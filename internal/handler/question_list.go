package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/scottbass3/quizz-backend/internal/domain"
	"github.com/scottbass3/quizz-backend/internal/store"
)

type QuestionListHandler struct {
	store  store.QuestionListStore
	themes store.ThemeStore
	logger *slog.Logger
}

func NewQuestionListHandler(s store.QuestionListStore, themes store.ThemeStore, logger *slog.Logger) *QuestionListHandler {
	return &QuestionListHandler{store: s, themes: themes, logger: logger}
}

// POST /question-lists
func (h *QuestionListHandler) Create(w http.ResponseWriter, r *http.Request) {
	a := extractActor(r)

	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Visibility  string `json:"visibility"` // "public" | "private"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Visibility != "public" && req.Visibility != "private" {
		writeError(w, http.StatusBadRequest, "visibility must be 'public' or 'private'")
		return
	}

	// Business rules:
	// - Only admins can create public lists.
	// - Only users can create private lists.
	if req.Visibility == "public" && a.Type != domain.ActorTypeAdmin {
		writeError(w, http.StatusForbidden, "only admins can create public question lists")
		return
	}
	if req.Visibility == "private" && a.Type != domain.ActorTypeUser {
		writeError(w, http.StatusForbidden, "only users can create private question lists")
		return
	}

	ownerID := ""
	if req.Visibility == "private" {
		ownerID = a.ID
	}

	now := time.Now().UTC()
	rec := store.QuestionListRecord{
		ID:          uuid.NewString(),
		Name:        req.Name,
		Description: req.Description,
		Visibility:  req.Visibility,
		OwnerType:   string(a.Type),
		OwnerID:     ownerID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := h.store.CreateQuestionList(r.Context(), rec); err != nil {
		h.logger.Error("create question list", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create question list")
		return
	}

	writeJSON(w, http.StatusCreated, rec)
}

// GET /question-lists/public
func (h *QuestionListHandler) ListPublic(w http.ResponseWriter, r *http.Request) {
	lists, err := h.store.ListPublicQuestionLists(r.Context())
	if err != nil {
		h.logger.Error("list public question lists", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list question lists")
		return
	}
	if lists == nil {
		lists = []store.QuestionListRecord{}
	}
	writeJSON(w, http.StatusOK, lists)
}

// GET /question-lists/private
func (h *QuestionListHandler) ListPrivate(w http.ResponseWriter, r *http.Request) {
	a := extractActor(r)
	if a.Type != domain.ActorTypeUser {
		writeError(w, http.StatusForbidden, "only users can access private question lists")
		return
	}

	lists, err := h.store.ListPrivateQuestionLists(r.Context(), a.ID)
	if err != nil {
		h.logger.Error("list private question lists", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list question lists")
		return
	}
	if lists == nil {
		lists = []store.QuestionListRecord{}
	}
	writeJSON(w, http.StatusOK, lists)
}

// GET /question-lists/{id}
func (h *QuestionListHandler) Get(w http.ResponseWriter, r *http.Request) {
	a := extractActor(r)
	id := chi.URLParam(r, "id")

	list, err := h.store.GetQuestionList(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "question list not found")
		return
	}
	if !canReadList(a, list) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	writeJSON(w, http.StatusOK, list)
}

// GET /question-lists/{id}/questions
func (h *QuestionListHandler) ListQuestions(w http.ResponseWriter, r *http.Request) {
	a := extractActor(r)
	id := chi.URLParam(r, "id")

	list, err := h.store.GetQuestionList(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "question list not found")
		return
	}
	if !canReadList(a, list) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	questions, err := h.store.ListQuestions(r.Context(), id)
	if err != nil {
		h.logger.Error("list questions", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list questions")
		return
	}

	// Optional filter: ?theme_id=<id>, or ?theme_id=none for unthemed questions.
	// The correct answer is only shown to actors who can edit the list, so
	// players cannot read the answers of a public list before a game.
	out := []store.QuestionRecord{}
	themeFilter, filtered := r.URL.Query()["theme_id"]
	editor := canEditList(a, list)
	for _, q := range questions {
		if filtered && !matchesTheme(q, themeFilter[0]) {
			continue
		}
		if !editor {
			q.CorrectOptionID = ""
		}
		out = append(out, q)
	}
	writeJSON(w, http.StatusOK, out)
}

// matchesTheme implements the theme_id filter of ListQuestions.
func matchesTheme(q store.QuestionRecord, filter string) bool {
	if filter == "none" {
		return q.ThemeID == ""
	}
	return q.ThemeID == filter
}

// questionRequest is the body of POST and PUT on questions.
type questionRequest struct {
	Text    string `json:"text"`
	Options []struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	} `json:"options"`
	CorrectOptionID string `json:"correct_option_id"`
	// ThemeID is optional: empty or null means no theme.
	ThemeID string `json:"theme_id"`
}

// decodeQuestion validates a question body. Empty option IDs are generated;
// correct_option_id must match one of the options.
func decodeQuestion(w http.ResponseWriter, r *http.Request) (*questionRequest, []store.OptionRecord, bool) {
	var req questionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return nil, nil, false
	}
	if req.Text == "" || len(req.Options) < 2 || req.CorrectOptionID == "" {
		writeError(w, http.StatusBadRequest, "text, at least 2 options and correct_option_id are required")
		return nil, nil, false
	}

	opts := make([]store.OptionRecord, len(req.Options))
	seen := make(map[string]bool, len(req.Options))
	for i, o := range req.Options {
		id := o.ID
		if id == "" {
			id = uuid.NewString()
		}
		if seen[id] {
			writeError(w, http.StatusBadRequest, "option ids must be unique")
			return nil, nil, false
		}
		seen[id] = true
		opts[i] = store.OptionRecord{ID: id, Text: o.Text}
	}
	if !seen[req.CorrectOptionID] {
		writeError(w, http.StatusBadRequest, "correct_option_id must match one of the options")
		return nil, nil, false
	}
	return &req, opts, true
}

// checkTheme validates that themeID (if set) is a global theme or a custom
// theme of listID. It writes a 400 with a code and returns false otherwise.
func (h *QuestionListHandler) checkTheme(w http.ResponseWriter, r *http.Request, themeID, listID string) bool {
	if themeID == "" {
		return true
	}
	t, err := h.themes.GetTheme(r.Context(), themeID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErrorCode(w, http.StatusBadRequest, "theme_not_found", "theme not found")
		return false
	case err != nil:
		h.logger.Error("get theme for question", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check theme")
		return false
	case t.QuestionListID != "" && t.QuestionListID != listID:
		writeErrorCode(w, http.StatusBadRequest, "theme_from_another_list", "theme belongs to another question list")
		return false
	}
	return true
}

// loadEditableList loads the {id} list and checks the actor may edit it.
func (h *QuestionListHandler) loadEditableList(w http.ResponseWriter, r *http.Request) (*store.QuestionListRecord, bool) {
	a := extractActor(r)
	list, err := h.store.GetQuestionList(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "question list not found")
		return nil, false
	}
	// Public list: only admins. Private list: only the owner.
	if !canEditList(a, list) {
		if list.Visibility == string(domain.ListVisibilityPublic) {
			writeError(w, http.StatusForbidden, "only admins can edit public lists")
		} else {
			writeError(w, http.StatusForbidden, "access denied")
		}
		return nil, false
	}
	return list, true
}

// POST /question-lists/{id}/questions
func (h *QuestionListHandler) AddQuestion(w http.ResponseWriter, r *http.Request) {
	list, ok := h.loadEditableList(w, r)
	if !ok {
		return
	}
	req, opts, ok := decodeQuestion(w, r)
	if !ok || !h.checkTheme(w, r, req.ThemeID, list.ID) {
		return
	}

	// Determine next order_index.
	existing, err := h.store.ListQuestions(r.Context(), list.ID)
	if err != nil {
		h.logger.Error("list questions for order index", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to add question")
		return
	}

	q := store.QuestionRecord{
		ID:              uuid.NewString(),
		QuestionListID:  list.ID,
		Text:            req.Text,
		Options:         opts,
		CorrectOptionID: req.CorrectOptionID,
		OrderIndex:      len(existing),
		ThemeID:         req.ThemeID,
	}

	if err := h.store.CreateQuestion(r.Context(), q); err != nil {
		h.logger.Error("create question", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create question")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"question_id": q.ID})
}

// PUT /question-lists/{id}/questions/{questionID}
// Replaces text, options, correct option and theme. The position is kept.
func (h *QuestionListHandler) UpdateQuestion(w http.ResponseWriter, r *http.Request) {
	list, ok := h.loadEditableList(w, r)
	if !ok {
		return
	}
	existing, err := h.store.GetQuestion(r.Context(), chi.URLParam(r, "questionID"))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		h.logger.Error("get question", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load question")
		return
	}
	if err != nil || existing.QuestionListID != list.ID {
		writeError(w, http.StatusNotFound, "question not found")
		return
	}
	req, opts, ok := decodeQuestion(w, r)
	if !ok || !h.checkTheme(w, r, req.ThemeID, list.ID) {
		return
	}

	existing.Text = req.Text
	existing.Options = opts
	existing.CorrectOptionID = req.CorrectOptionID
	existing.ThemeID = req.ThemeID
	if err := h.store.UpdateQuestion(r.Context(), *existing); err != nil {
		h.logger.Error("update question", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update question")
		return
	}

	updated, err := h.store.GetQuestion(r.Context(), existing.ID)
	if err != nil {
		h.logger.Error("reload question", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load question")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// PUT /question-lists/{id} (list editors)
// Renames the list and/or changes its description. Visibility cannot change.
func (h *QuestionListHandler) UpdateList(w http.ResponseWriter, r *http.Request) {
	list, ok := h.loadEditableList(w, r)
	if !ok {
		return
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	list.Name = strings.TrimSpace(req.Name)
	list.Description = req.Description
	list.UpdatedAt = time.Now().UTC()
	if err := h.store.UpdateQuestionList(r.Context(), *list); err != nil {
		h.logger.Error("update question list", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update question list")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// DELETE /question-lists/{id} (list editors)
// Deletes the list, its questions and its custom themes. Games already
// created from it are not affected (they hold their own copy).
func (h *QuestionListHandler) DeleteList(w http.ResponseWriter, r *http.Request) {
	list, ok := h.loadEditableList(w, r)
	if !ok {
		return
	}
	if err := h.store.DeleteQuestionList(r.Context(), list.ID); err != nil {
		h.logger.Error("delete question list", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete question list")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /question-lists/{id}/questions/{questionID} (list editors)
// The following questions move up one position.
func (h *QuestionListHandler) DeleteQuestion(w http.ResponseWriter, r *http.Request) {
	list, ok := h.loadEditableList(w, r)
	if !ok {
		return
	}
	q, err := h.store.GetQuestion(r.Context(), chi.URLParam(r, "questionID"))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		h.logger.Error("get question", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load question")
		return
	}
	if err != nil || q.QuestionListID != list.ID {
		writeError(w, http.StatusNotFound, "question not found")
		return
	}
	if err := h.store.DeleteQuestion(r.Context(), q.ID); err != nil {
		h.logger.Error("delete question", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete question")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PUT /question-lists/{id}/questions/order (list editors)
// Body: {"question_ids": [...]} listing every question of the list exactly
// once, in the new order. Answers the reordered questions.
func (h *QuestionListHandler) ReorderQuestions(w http.ResponseWriter, r *http.Request) {
	list, ok := h.loadEditableList(w, r)
	if !ok {
		return
	}
	var req struct {
		QuestionIDs []string `json:"question_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	existing, err := h.store.ListQuestions(r.Context(), list.ID)
	if err != nil {
		h.logger.Error("list questions for reorder", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to reorder questions")
		return
	}
	if !isPermutation(req.QuestionIDs, existing) {
		writeErrorCode(w, http.StatusBadRequest, "invalid_order",
			"question_ids must list every question of the list exactly once")
		return
	}
	if err := h.store.ReorderQuestions(r.Context(), list.ID, req.QuestionIDs); err != nil {
		h.logger.Error("reorder questions", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to reorder questions")
		return
	}
	questions, err := h.store.ListQuestions(r.Context(), list.ID)
	if err != nil {
		h.logger.Error("list questions after reorder", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list questions")
		return
	}
	if questions == nil {
		questions = []store.QuestionRecord{}
	}
	writeJSON(w, http.StatusOK, questions)
}

// isPermutation reports whether ids names each question exactly once.
func isPermutation(ids []string, questions []store.QuestionRecord) bool {
	if len(ids) != len(questions) {
		return false
	}
	remaining := make(map[string]bool, len(questions))
	for _, q := range questions {
		remaining[q.ID] = true
	}
	for _, id := range ids {
		if !remaining[id] {
			return false // unknown or duplicate
		}
		delete(remaining, id)
	}
	return true
}

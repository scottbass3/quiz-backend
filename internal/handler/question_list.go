package handler

// NOTE: Authentication is not implemented.
// Identity is simulated via dev-only HTTP headers:
//
//	X-Debug-Actor-Type: admin | user  (default: "user")
//	X-Debug-Actor-Id:   <arbitrary string>  (default: "anonymous")
//
// These headers MUST NOT be trusted in a production deployment.
// Replace this mechanism with real auth middleware before going to production.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/scottbass3/quizz-backend/internal/domain"
	"github.com/scottbass3/quizz-backend/internal/store"
)

type QuestionListHandler struct {
	store  store.QuestionListStore
	logger *slog.Logger
}

func NewQuestionListHandler(s store.QuestionListStore, logger *slog.Logger) *QuestionListHandler {
	return &QuestionListHandler{store: s, logger: logger}
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
	if list.Visibility == "private" && list.OwnerID != a.ID {
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
	if list.Visibility == "private" && list.OwnerID != a.ID {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	questions, err := h.store.ListQuestions(r.Context(), id)
	if err != nil {
		h.logger.Error("list questions", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list questions")
		return
	}
	if questions == nil {
		questions = []store.QuestionRecord{}
	}
	writeJSON(w, http.StatusOK, questions)
}

// POST /question-lists/{id}/questions
func (h *QuestionListHandler) AddQuestion(w http.ResponseWriter, r *http.Request) {
	a := extractActor(r)
	listID := chi.URLParam(r, "id")

	list, err := h.store.GetQuestionList(r.Context(), listID)
	if err != nil {
		writeError(w, http.StatusNotFound, "question list not found")
		return
	}

	// Write-access rules:
	// - Public list: only admins can add questions.
	// - Private list: only the owner can add questions.
	if list.Visibility == "public" && a.Type != domain.ActorTypeAdmin {
		writeError(w, http.StatusForbidden, "only admins can add questions to public lists")
		return
	}
	if list.Visibility == "private" && list.OwnerID != a.ID {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	var req struct {
		Text    string `json:"text"`
		Options []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		} `json:"options"`
		CorrectOptionID string `json:"correct_option_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if req.Text == "" || len(req.Options) < 2 || req.CorrectOptionID == "" {
		writeError(w, http.StatusBadRequest, "text, at least 2 options and correct_option_id are required")
		return
	}

	// Determine next order_index.
	existing, err := h.store.ListQuestions(r.Context(), listID)
	if err != nil {
		h.logger.Error("list questions for order index", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to add question")
		return
	}

	opts := make([]store.OptionRecord, len(req.Options))
	for i, o := range req.Options {
		id := o.ID
		if id == "" {
			id = uuid.NewString()
		}
		opts[i] = store.OptionRecord{ID: id, Text: o.Text}
	}

	q := store.QuestionRecord{
		ID:              uuid.NewString(),
		QuestionListID:  listID,
		Text:            req.Text,
		Options:         opts,
		CorrectOptionID: req.CorrectOptionID,
		OrderIndex:      len(existing),
	}

	if err := h.store.CreateQuestion(r.Context(), q); err != nil {
		h.logger.Error("create question", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create question")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"question_id": q.ID})
}

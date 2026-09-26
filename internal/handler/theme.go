package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/scottbass3/quizz-backend/internal/domain"
	"github.com/scottbass3/quizz-backend/internal/store"
)

const maxThemeNameLen = 100

// ThemeHandler serves global themes (/themes, admin-managed) and the custom
// themes of a question list (/question-lists/{id}/themes, managed by whoever
// can edit the list). Each route only sees themes of its own scope.
type ThemeHandler struct {
	themes store.ThemeStore
	lists  store.QuestionListStore
	logger *slog.Logger
}

func NewThemeHandler(themes store.ThemeStore, lists store.QuestionListStore, logger *slog.Logger) *ThemeHandler {
	return &ThemeHandler{themes: themes, lists: lists, logger: logger}
}

type themeRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// decodeThemeRequest reads and validates a create/update body.
func decodeThemeRequest(w http.ResponseWriter, r *http.Request) (themeRequest, bool) {
	var req themeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return req, false
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || utf8.RuneCountInString(req.Name) > maxThemeNameLen {
		writeError(w, http.StatusBadRequest, "name is required (at most 100 characters)")
		return req, false
	}
	return req, true
}

// ── Global themes ────────────────────────────────────────────────────────────

// GET /themes
func (h *ThemeHandler) ListGlobal(w http.ResponseWriter, r *http.Request) {
	themes, err := h.themes.ListGlobalThemes(r.Context())
	if err != nil {
		h.logger.Error("list global themes", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list themes")
		return
	}
	writeThemes(w, themes)
}

// GET /themes/{themeID}
func (h *ThemeHandler) GetGlobal(w http.ResponseWriter, r *http.Request) {
	if t, ok := h.loadTheme(w, r, ""); ok {
		writeJSON(w, http.StatusOK, t)
	}
}

// POST /themes (admin)
func (h *ThemeHandler) CreateGlobal(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	h.create(w, r, "")
}

// PUT /themes/{themeID} (admin)
func (h *ThemeHandler) UpdateGlobal(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	h.update(w, r, "")
}

// DELETE /themes/{themeID} (admin)
func (h *ThemeHandler) DeleteGlobal(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	h.delete(w, r, "")
}

func (h *ThemeHandler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if extractActor(r).Type != domain.ActorTypeAdmin {
		writeError(w, http.StatusForbidden, "only admins can manage global themes")
		return false
	}
	return true
}

// ── Question-list custom themes ──────────────────────────────────────────────

// GET /question-lists/{id}/themes
func (h *ThemeHandler) ListForList(w http.ResponseWriter, r *http.Request) {
	list, ok := h.loadList(w, r, false)
	if !ok {
		return
	}
	themes, err := h.themes.ListQuestionListThemes(r.Context(), list.ID)
	if err != nil {
		h.logger.Error("list question list themes", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list themes")
		return
	}
	writeThemes(w, themes)
}

// GET /question-lists/{id}/themes/{themeID}
func (h *ThemeHandler) GetForList(w http.ResponseWriter, r *http.Request) {
	list, ok := h.loadList(w, r, false)
	if !ok {
		return
	}
	if t, ok := h.loadTheme(w, r, list.ID); ok {
		writeJSON(w, http.StatusOK, t)
	}
}

// POST /question-lists/{id}/themes (list editors)
func (h *ThemeHandler) CreateForList(w http.ResponseWriter, r *http.Request) {
	if list, ok := h.loadList(w, r, true); ok {
		h.create(w, r, list.ID)
	}
}

// PUT /question-lists/{id}/themes/{themeID} (list editors)
func (h *ThemeHandler) UpdateForList(w http.ResponseWriter, r *http.Request) {
	if list, ok := h.loadList(w, r, true); ok {
		h.update(w, r, list.ID)
	}
}

// DELETE /question-lists/{id}/themes/{themeID} (list editors)
func (h *ThemeHandler) DeleteForList(w http.ResponseWriter, r *http.Request) {
	if list, ok := h.loadList(w, r, true); ok {
		h.delete(w, r, list.ID)
	}
}

// loadList loads the {id} list and checks read (or edit) access.
func (h *ThemeHandler) loadList(w http.ResponseWriter, r *http.Request, edit bool) (*store.QuestionListRecord, bool) {
	a := extractActor(r)
	list, err := h.lists.GetQuestionList(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "question list not found")
		return nil, false
	}
	if !canReadList(a, list) || (edit && !canEditList(a, list)) {
		writeError(w, http.StatusForbidden, "access denied")
		return nil, false
	}
	return list, true
}

// ── Shared operations (listID "" = global scope) ─────────────────────────────

// loadTheme loads {themeID} and hides it (404) unless it belongs to the
// expected scope: global when listID is empty, that list otherwise.
func (h *ThemeHandler) loadTheme(w http.ResponseWriter, r *http.Request, listID string) (*store.ThemeRecord, bool) {
	t, err := h.themes.GetTheme(r.Context(), chi.URLParam(r, "themeID"))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		h.logger.Error("get theme", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load theme")
		return nil, false
	}
	if err != nil || t.QuestionListID != listID {
		writeError(w, http.StatusNotFound, "theme not found")
		return nil, false
	}
	return t, true
}

func (h *ThemeHandler) create(w http.ResponseWriter, r *http.Request, listID string) {
	req, ok := decodeThemeRequest(w, r)
	if !ok {
		return
	}
	now := time.Now().UTC()
	t := store.ThemeRecord{
		ID:             uuid.NewString(),
		Scope:          store.ThemeScopeGlobal,
		QuestionListID: listID,
		Name:           req.Name,
		Description:    req.Description,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if listID != "" {
		t.Scope = store.ThemeScopeList
	}
	if err := h.themes.CreateTheme(r.Context(), t); err != nil {
		h.writeStoreError(w, "create theme", err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (h *ThemeHandler) update(w http.ResponseWriter, r *http.Request, listID string) {
	t, ok := h.loadTheme(w, r, listID)
	if !ok {
		return
	}
	req, ok := decodeThemeRequest(w, r)
	if !ok {
		return
	}
	t.Name, t.Description, t.UpdatedAt = req.Name, req.Description, time.Now().UTC()
	if err := h.themes.UpdateTheme(r.Context(), *t); err != nil {
		h.writeStoreError(w, "update theme", err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *ThemeHandler) delete(w http.ResponseWriter, r *http.Request, listID string) {
	t, ok := h.loadTheme(w, r, listID)
	if !ok {
		return
	}
	if err := h.themes.DeleteTheme(r.Context(), t.ID); err != nil {
		h.writeStoreError(w, "delete theme", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ThemeHandler) writeStoreError(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, store.ErrConflict):
		writeErrorCode(w, http.StatusConflict, "theme_name_taken", "a theme with this name already exists")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "theme not found")
	default:
		h.logger.Error(op, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to "+op)
	}
}

func writeThemes(w http.ResponseWriter, themes []store.ThemeRecord) {
	if themes == nil {
		themes = []store.ThemeRecord{}
	}
	writeJSON(w, http.StatusOK, themes)
}

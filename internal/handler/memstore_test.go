package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/scottbass3/quizz-backend/internal/auth"
	"github.com/scottbass3/quizz-backend/internal/store"
)

// memStore is an in-memory QuestionListStore + ThemeStore that mirrors the
// Postgres constraints: theme names unique per scope (case-insensitive) and
// questions unassigned when their theme is deleted.
type memStore struct {
	mu        sync.Mutex
	lists     map[string]store.QuestionListRecord
	questions map[string]store.QuestionRecord
	themes    map[string]store.ThemeRecord
}

func newMemStore() *memStore {
	return &memStore{
		lists:     map[string]store.QuestionListRecord{},
		questions: map[string]store.QuestionRecord{},
		themes:    map[string]store.ThemeRecord{},
	}
}

func (m *memStore) CreateQuestionList(_ context.Context, l store.QuestionListRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lists[l.ID] = l
	return nil
}

func (m *memStore) GetQuestionList(_ context.Context, id string) (*store.QuestionListRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.lists[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &l, nil
}

func (m *memStore) ListPublicQuestionLists(context.Context) ([]store.QuestionListRecord, error) {
	return nil, nil
}

func (m *memStore) ListPrivateQuestionLists(context.Context, string) ([]store.QuestionListRecord, error) {
	return nil, nil
}

func (m *memStore) CreateQuestion(_ context.Context, q store.QuestionRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.questions[q.ID] = q
	return nil
}

func (m *memStore) GetQuestion(_ context.Context, id string) (*store.QuestionRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q, ok := m.questions[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	m.fillTheme(&q)
	return &q, nil
}

func (m *memStore) UpdateQuestion(_ context.Context, q store.QuestionRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.questions[q.ID]
	if !ok {
		return store.ErrNotFound
	}
	q.QuestionListID, q.OrderIndex = old.QuestionListID, old.OrderIndex
	m.questions[q.ID] = q
	return nil
}

func (m *memStore) ListQuestions(_ context.Context, listID string) ([]store.QuestionRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.QuestionRecord
	for _, q := range m.questions {
		if q.QuestionListID == listID {
			m.fillTheme(&q)
			out = append(out, q)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OrderIndex < out[j].OrderIndex })
	return out, nil
}

// fillTheme sets q.Theme from q.ThemeID. Caller holds mu.
func (m *memStore) fillTheme(q *store.QuestionRecord) {
	q.Theme = nil
	if t, ok := m.themes[q.ThemeID]; ok {
		q.Theme = &store.ThemeRef{ID: t.ID, Name: t.Name, Scope: t.Scope}
	}
}

func (m *memStore) nameTaken(t store.ThemeRecord) bool {
	for _, o := range m.themes {
		if o.ID != t.ID && o.QuestionListID == t.QuestionListID && strings.EqualFold(o.Name, t.Name) {
			return true
		}
	}
	return false
}

func (m *memStore) CreateTheme(_ context.Context, t store.ThemeRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nameTaken(t) {
		return store.ErrConflict
	}
	m.themes[t.ID] = t
	return nil
}

func (m *memStore) GetTheme(_ context.Context, id string) (*store.ThemeRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.themes[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &t, nil
}

func (m *memStore) listThemes(listID string) []store.ThemeRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.ThemeRecord
	for _, t := range m.themes {
		if t.QuestionListID == listID {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func (m *memStore) ListGlobalThemes(context.Context) ([]store.ThemeRecord, error) {
	return m.listThemes(""), nil
}

func (m *memStore) ListQuestionListThemes(_ context.Context, listID string) ([]store.ThemeRecord, error) {
	return m.listThemes(listID), nil
}

func (m *memStore) UpdateTheme(_ context.Context, t store.ThemeRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.themes[t.ID]
	if !ok {
		return store.ErrNotFound
	}
	t.QuestionListID, t.Scope = old.QuestionListID, old.Scope
	if m.nameTaken(t) {
		return store.ErrConflict
	}
	m.themes[t.ID] = t
	return nil
}

func (m *memStore) DeleteTheme(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.themes[id]; !ok {
		return store.ErrNotFound
	}
	delete(m.themes, id)
	for qid, q := range m.questions {
		if q.ThemeID == id {
			q.ThemeID = ""
			m.questions[qid] = q
		}
	}
	return nil
}

// catalogServer serves the question list and theme routes as wired in
// app.New, behind the dev-mode auth middleware.
func catalogServer(t *testing.T, m *memStore) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	qlH := NewQuestionListHandler(m, logger)
	themeH := NewThemeHandler(m, m, logger)

	r := chi.NewRouter()
	r.Use(auth.Middleware([]byte("test"), false))
	r.Route("/question-lists", func(r chi.Router) {
		r.Post("/", qlH.Create)
		r.Get("/{id}/questions", qlH.ListQuestions)
		r.Post("/{id}/questions", qlH.AddQuestion)
		r.Get("/{id}/themes", themeH.ListForList)
		r.Post("/{id}/themes", themeH.CreateForList)
		r.Get("/{id}/themes/{themeID}", themeH.GetForList)
		r.Put("/{id}/themes/{themeID}", themeH.UpdateForList)
		r.Delete("/{id}/themes/{themeID}", themeH.DeleteForList)
	})
	r.Route("/themes", func(r chi.Router) {
		r.Get("/", themeH.ListGlobal)
		r.Post("/", themeH.CreateGlobal)
		r.Get("/{themeID}", themeH.GetGlobal)
		r.Put("/{themeID}", themeH.UpdateGlobal)
		r.Delete("/{themeID}", themeH.DeleteGlobal)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

// actor identifies the caller through the dev-mode debug headers.
type actor struct{ typ, id string }

var (
	admin = actor{"admin", "admin-1"}
	alice = actor{"user", "alice"}
	bob   = actor{"user", "bob"}
)

// call sends a JSON request as a and decodes the JSON response into out (if non-nil).
func call(t *testing.T, srv *httptest.Server, a actor, method, path, body string, out any) int {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	req.Header.Set("X-Debug-Actor-Type", a.typ)
	req.Header.Set("X-Debug-Actor-Id", a.id)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

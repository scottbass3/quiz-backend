package handler

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/scottbass3/quizz-backend/internal/store"
)

func questionBody(themeID string) string {
	theme := ""
	if themeID != "" {
		theme = fmt.Sprintf(`,"theme_id":%q`, themeID)
	}
	return `{"text":"2+2?","options":[{"id":"a","text":"3"},{"id":"b","text":"4"}],"correct_option_id":"b"` + theme + `}`
}

// themedCatalog sets up two public lists, a global theme and a custom theme of each list.
type themedCatalog struct {
	l1, l2              string
	global, own, others string
}

func newThemedCatalog(t *testing.T, call func(a actor, method, path, body string, out any) int) themedCatalog {
	t.Helper()
	var l1, l2 store.QuestionListRecord
	var global, own, others store.ThemeRecord
	call(admin, "POST", "/question-lists", `{"name":"L1","visibility":"public"}`, &l1)
	call(admin, "POST", "/question-lists", `{"name":"L2","visibility":"public"}`, &l2)
	call(admin, "POST", "/themes", `{"name":"History"}`, &global)
	call(admin, "POST", "/question-lists/"+l1.ID+"/themes", `{"name":"Local"}`, &own)
	call(admin, "POST", "/question-lists/"+l2.ID+"/themes", `{"name":"Elsewhere"}`, &others)
	return themedCatalog{l1: l1.ID, l2: l2.ID, global: global.ID, own: own.ID, others: others.ID}
}

func TestQuestionThemes(t *testing.T) {
	srv := catalogServer(t, newMemStore())
	do := func(a actor, method, path, body string, out any) int { return call(t, srv, a, method, path, body, out) }
	c := newThemedCatalog(t, do)
	questions := "/question-lists/" + c.l1 + "/questions"

	// Theme is optional, and may be global or a custom theme of the same list.
	cases := []struct {
		name, themeID string
		wantCode      int
		wantErrCode   string
	}{
		{"no theme", "", http.StatusCreated, ""},
		{"global theme", c.global, http.StatusCreated, ""},
		{"own list theme", c.own, http.StatusCreated, ""},
		{"other list theme", c.others, http.StatusBadRequest, "theme_from_another_list"},
		{"unknown theme", "nope", http.StatusBadRequest, "theme_not_found"},
	}
	for _, tc := range cases {
		var body map[string]string
		if code := do(admin, "POST", questions, questionBody(tc.themeID), &body); code != tc.wantCode || body["code"] != tc.wantErrCode {
			t.Errorf("%s: expected %d %q, got %d %v", tc.name, tc.wantCode, tc.wantErrCode, code, body)
		}
	}

	var list []store.QuestionRecord
	do(admin, "GET", questions, "", &list)
	if len(list) != 3 {
		t.Fatalf("expected 3 questions, got %d", len(list))
	}
	if list[0].Theme != nil {
		t.Errorf("question without theme should have theme null, got %+v", list[0].Theme)
	}
	if got := list[1].Theme; got == nil || got.ID != c.global || got.Scope != store.ThemeScopeGlobal || got.Name != "History" {
		t.Errorf("expected global theme History, got %+v", got)
	}
	if got := list[2].Theme; got == nil || got.ID != c.own || got.Scope != store.ThemeScopeList {
		t.Errorf("expected list theme, got %+v", got)
	}

	// Filtering.
	var filtered []store.QuestionRecord
	do(admin, "GET", questions+"?theme_id="+c.global, "", &filtered)
	if len(filtered) != 1 || filtered[0].ID != list[1].ID {
		t.Errorf("theme filter: got %+v", filtered)
	}
	do(admin, "GET", questions+"?theme_id=none", "", &filtered)
	if len(filtered) != 1 || filtered[0].ID != list[0].ID {
		t.Errorf("none filter: got %+v", filtered)
	}

	// Deleting a theme leaves its questions without theme.
	do(admin, "DELETE", "/themes/"+c.global, "", nil)
	do(admin, "GET", questions, "", &list)
	if list[1].Theme != nil {
		t.Errorf("question should lose its deleted theme, got %+v", list[1].Theme)
	}
}

func TestUpdateQuestion(t *testing.T) {
	srv := catalogServer(t, newMemStore())
	do := func(a actor, method, path, body string, out any) int { return call(t, srv, a, method, path, body, out) }
	c := newThemedCatalog(t, do)

	var created map[string]string
	do(admin, "POST", "/question-lists/"+c.l1+"/questions", questionBody(""), &created)
	path := "/question-lists/" + c.l1 + "/questions/" + created["question_id"]

	// Assign a theme and change the content; the position is kept.
	var q store.QuestionRecord
	body := `{"text":"Capital of France?","options":[{"id":"x","text":"Paris"},{"id":"y","text":"Rome"}],"correct_option_id":"x","theme_id":"` + c.own + `"}`
	if code := do(admin, "PUT", path, body, &q); code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d", code)
	}
	if q.Text != "Capital of France?" || q.CorrectOptionID != "x" || q.Theme == nil || q.Theme.ID != c.own || q.OrderIndex != 0 {
		t.Fatalf("unexpected updated question %+v (theme %+v)", q, q.Theme)
	}

	// Omitting theme_id removes the theme.
	if code := do(admin, "PUT", path, questionBody(""), &q); code != http.StatusOK || q.Theme != nil {
		t.Fatalf("remove theme: got %d, theme %+v", code, q.Theme)
	}

	checks := []struct {
		name     string
		who      actor
		path     string
		body     string
		wantCode int
	}{
		{"theme of another list", admin, path, questionBody(c.others), http.StatusBadRequest},
		{"correct option not in options", admin, path, `{"text":"t","options":[{"id":"a"},{"id":"b"}],"correct_option_id":"z"}`, http.StatusBadRequest},
		{"duplicate option ids", admin, path, `{"text":"t","options":[{"id":"a"},{"id":"a"}],"correct_option_id":"a"}`, http.StatusBadRequest},
		{"not an editor", alice, path, questionBody(""), http.StatusForbidden},
		{"question of another list", admin, "/question-lists/" + c.l2 + "/questions/" + created["question_id"], questionBody(""), http.StatusNotFound},
		{"unknown question", admin, "/question-lists/" + c.l1 + "/questions/nope", questionBody(""), http.StatusNotFound},
	}
	for _, tc := range checks {
		if code := do(tc.who, "PUT", tc.path, tc.body, nil); code != tc.wantCode {
			t.Errorf("%s: expected %d, got %d", tc.name, tc.wantCode, code)
		}
	}
}

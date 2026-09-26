package handler

import (
	"net/http"
	"testing"

	"github.com/scottbass3/quizz-backend/internal/store"
)

func TestGlobalThemes(t *testing.T) {
	srv := catalogServer(t, newMemStore())

	if code := call(t, srv, alice, "POST", "/themes", `{"name":"History"}`, nil); code != http.StatusForbidden {
		t.Fatalf("user create: expected 403, got %d", code)
	}

	var geo store.ThemeRecord
	if code := call(t, srv, admin, "POST", "/themes", `{"name":" Geography ","description":"Maps"}`, &geo); code != http.StatusCreated {
		t.Fatalf("admin create: expected 201, got %d", code)
	}
	if geo.Name != "Geography" || geo.Scope != store.ThemeScopeGlobal || geo.QuestionListID != "" {
		t.Fatalf("unexpected theme %+v", geo)
	}

	var errBody map[string]string
	if code := call(t, srv, admin, "POST", "/themes", `{"name":"geography"}`, &errBody); code != http.StatusConflict || errBody["code"] != "theme_name_taken" {
		t.Fatalf("duplicate name: expected 409 theme_name_taken, got %d %v", code, errBody)
	}
	if code := call(t, srv, admin, "POST", "/themes", `{"name":"  "}`, nil); code != http.StatusBadRequest {
		t.Fatalf("empty name: expected 400, got %d", code)
	}

	var all []store.ThemeRecord
	if code := call(t, srv, alice, "GET", "/themes", "", &all); code != http.StatusOK || len(all) != 1 {
		t.Fatalf("list as user: expected 1 theme, got %d %v", code, all)
	}

	var updated store.ThemeRecord
	if code := call(t, srv, admin, "PUT", "/themes/"+geo.ID, `{"name":"World geography"}`, &updated); code != http.StatusOK || updated.Name != "World geography" {
		t.Fatalf("update: got %d %+v", code, updated)
	}
	if code := call(t, srv, alice, "PUT", "/themes/"+geo.ID, `{"name":"x"}`, nil); code != http.StatusForbidden {
		t.Fatalf("user update: expected 403, got %d", code)
	}
	if code := call(t, srv, alice, "DELETE", "/themes/"+geo.ID, "", nil); code != http.StatusForbidden {
		t.Fatalf("user delete: expected 403, got %d", code)
	}
	if code := call(t, srv, admin, "DELETE", "/themes/"+geo.ID, "", nil); code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d", code)
	}
	if code := call(t, srv, admin, "GET", "/themes/"+geo.ID, "", nil); code != http.StatusNotFound {
		t.Fatalf("get deleted: expected 404, got %d", code)
	}
}

func TestListThemes_AccessRules(t *testing.T) {
	m := newMemStore()
	srv := catalogServer(t, m)

	var pub, priv store.QuestionListRecord
	call(t, srv, admin, "POST", "/question-lists", `{"name":"Public","visibility":"public"}`, &pub)
	call(t, srv, alice, "POST", "/question-lists", `{"name":"Alice","visibility":"private"}`, &priv)

	// Public list: admins manage custom themes, users can only read them.
	var pubTheme store.ThemeRecord
	if code := call(t, srv, admin, "POST", "/question-lists/"+pub.ID+"/themes", `{"name":"Retro games"}`, &pubTheme); code != http.StatusCreated {
		t.Fatalf("admin create on public list: expected 201, got %d", code)
	}
	if pubTheme.Scope != store.ThemeScopeList || pubTheme.QuestionListID != pub.ID {
		t.Fatalf("unexpected list theme %+v", pubTheme)
	}
	if code := call(t, srv, alice, "POST", "/question-lists/"+pub.ID+"/themes", `{"name":"x"}`, nil); code != http.StatusForbidden {
		t.Fatalf("user create on public list: expected 403, got %d", code)
	}
	var listed []store.ThemeRecord
	if code := call(t, srv, bob, "GET", "/question-lists/"+pub.ID+"/themes", "", &listed); code != http.StatusOK || len(listed) != 1 {
		t.Fatalf("user read public list themes: got %d %v", code, listed)
	}

	// Private list: only the owner, for reads and writes.
	if code := call(t, srv, alice, "POST", "/question-lists/"+priv.ID+"/themes", `{"name":"Family"}`, nil); code != http.StatusCreated {
		t.Fatalf("owner create: expected 201, got %d", code)
	}
	for _, a := range []actor{bob, admin} {
		if code := call(t, srv, a, "POST", "/question-lists/"+priv.ID+"/themes", `{"name":"x"}`, nil); code != http.StatusForbidden {
			t.Fatalf("%s create on private list: expected 403, got %d", a.id, code)
		}
		if code := call(t, srv, a, "GET", "/question-lists/"+priv.ID+"/themes", "", nil); code != http.StatusForbidden {
			t.Fatalf("%s read private list themes: expected 403, got %d", a.id, code)
		}
	}
}

func TestListThemes_ScopesAreSeparate(t *testing.T) {
	srv := catalogServer(t, newMemStore())

	var l1, l2 store.QuestionListRecord
	call(t, srv, admin, "POST", "/question-lists", `{"name":"L1","visibility":"public"}`, &l1)
	call(t, srv, admin, "POST", "/question-lists", `{"name":"L2","visibility":"public"}`, &l2)

	var global, t1 store.ThemeRecord
	call(t, srv, admin, "POST", "/themes", `{"name":"History"}`, &global)
	// The same name is allowed as a global theme and in each list.
	if code := call(t, srv, admin, "POST", "/question-lists/"+l1.ID+"/themes", `{"name":"History"}`, &t1); code != http.StatusCreated {
		t.Fatalf("same name as a global theme: expected 201, got %d", code)
	}
	if code := call(t, srv, admin, "POST", "/question-lists/"+l2.ID+"/themes", `{"name":"history"}`, nil); code != http.StatusCreated {
		t.Fatalf("same name in another list: expected 201, got %d", code)
	}
	if code := call(t, srv, admin, "POST", "/question-lists/"+l1.ID+"/themes", `{"name":"HISTORY"}`, nil); code != http.StatusConflict {
		t.Fatalf("duplicate in the same list: expected 409, got %d", code)
	}

	// A theme is only reachable through its own scope.
	if code := call(t, srv, admin, "GET", "/themes/"+t1.ID, "", nil); code != http.StatusNotFound {
		t.Fatalf("list theme via /themes: expected 404, got %d", code)
	}
	if code := call(t, srv, admin, "GET", "/question-lists/"+l1.ID+"/themes/"+global.ID, "", nil); code != http.StatusNotFound {
		t.Fatalf("global theme via a list: expected 404, got %d", code)
	}
	if code := call(t, srv, admin, "PUT", "/question-lists/"+l2.ID+"/themes/"+t1.ID, `{"name":"x"}`, nil); code != http.StatusNotFound {
		t.Fatalf("list theme via another list: expected 404, got %d", code)
	}
	if code := call(t, srv, admin, "DELETE", "/question-lists/"+l1.ID+"/themes/"+t1.ID, "", nil); code != http.StatusNoContent {
		t.Fatalf("delete list theme: expected 204, got %d", code)
	}
}

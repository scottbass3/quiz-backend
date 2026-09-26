package auth_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scottbass3/quizz-backend/internal/auth"
	"github.com/scottbass3/quizz-backend/internal/domain"
)

func actorFor(t *testing.T, r *http.Request) *auth.Actor {
	t.Helper()
	var got *auth.Actor
	h := auth.Middleware([]byte("secret"), false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = auth.ActorFromContext(r.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), r)
	if got == nil {
		t.Fatal("middleware did not set an actor")
	}
	return got
}

func TestDebugActor_Headers(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/games/x", nil)
	r.Header.Set("X-Debug-Actor-Type", "admin")
	r.Header.Set("X-Debug-Actor-Id", "alice")

	a := actorFor(t, r)
	if a.Sub != "alice" || a.ActorType != domain.ActorTypeAdmin {
		t.Fatalf("unexpected actor %+v", a)
	}
}

func TestDebugActor_QueryFallback(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/ws?debugActorType=user&debugActorId=bob", nil)

	a := actorFor(t, r)
	if a.Sub != "bob" || a.ActorType != domain.ActorTypeUser {
		t.Fatalf("unexpected actor %+v", a)
	}
}

func TestDebugActor_HeadersWinOverQuery(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/ws?debugActorId=bob", nil)
	r.Header.Set("X-Debug-Actor-Id", "alice")

	if a := actorFor(t, r); a.Sub != "alice" {
		t.Fatalf("expected header identity, got %q", a.Sub)
	}
}

func TestDebugActor_Defaults(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/games/x", nil)

	a := actorFor(t, r)
	if a.Sub != "anonymous" || a.ActorType != domain.ActorTypeUser {
		t.Fatalf("unexpected actor %+v", a)
	}
}

func TestOIDCMode_Unauthenticated(t *testing.T) {
	h := auth.Middleware([]byte("secret"), true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run without a session")
	}))

	for name, cookie := range map[string]*http.Cookie{
		"no cookie":      nil,
		"invalid cookie": {Name: auth.SessionCookie, Value: "garbage"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/games/x", nil)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)

		if rec.Code != http.StatusUnauthorized || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%s: expected JSON 401, got %d %q", name, rec.Code, rec.Header().Get("Content-Type"))
		}
		if !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s: expected an error body, got %q", name, rec.Body.String())
		}
	}
}

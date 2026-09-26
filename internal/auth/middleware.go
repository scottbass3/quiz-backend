package auth

import (
	"encoding/json"
	"net/http"

	"github.com/scottbass3/quizz-backend/internal/domain"
)

// Middleware sets the authenticated Actor in the request context.
//
//   - oidcEnabled=true: requires a valid session cookie; responds 401 otherwise.
//   - oidcEnabled=false: reads X-Debug-Actor-Type / X-Debug-Actor-Id headers (dev mode).
func Middleware(secret []byte, oidcEnabled bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var actor *Actor

			if !oidcEnabled {
				actor = actorFromDebugHeaders(r)
			} else {
				cookie, err := r.Cookie(SessionCookie)
				if err != nil {
					unauthorized(w, "unauthenticated")
					return
				}
				a, err := ParseSessionToken(secret, cookie.Value)
				if err != nil {
					unauthorized(w, "invalid session")
					return
				}
				actor = a
			}

			next.ServeHTTP(w, r.WithContext(WithActor(r.Context(), actor)))
		})
	}
}

// actorFromDebugHeaders reads the dev identity from X-Debug-Actor-* headers.
// Browsers cannot set headers on a WebSocket handshake, so the
// debugActorType / debugActorId query parameters are accepted as a fallback.
func actorFromDebugHeaders(r *http.Request) *Actor {
	t := r.Header.Get("X-Debug-Actor-Type")
	id := r.Header.Get("X-Debug-Actor-Id")
	if t == "" {
		t = r.URL.Query().Get("debugActorType")
	}
	if id == "" {
		id = r.URL.Query().Get("debugActorId")
	}
	if t != string(domain.ActorTypeAdmin) && t != string(domain.ActorTypeUser) {
		t = string(domain.ActorTypeUser)
	}
	if id == "" {
		id = "anonymous"
	}
	return &Actor{
		Sub:       id,
		Name:      id,
		Email:     "",
		ActorType: domain.ActorType(t),
	}
}

// unauthorized writes a JSON 401. (http.Error would reset the Content-Type
// to text/plain.)
func unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

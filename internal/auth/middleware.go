package auth

import (
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
					w.Header().Set("Content-Type", "application/json")
					http.Error(w, `{"error":"unauthenticated"}`, http.StatusUnauthorized)
					return
				}
				a, err := ParseSessionToken(secret, cookie.Value)
				if err != nil {
					w.Header().Set("Content-Type", "application/json")
					http.Error(w, `{"error":"invalid session"}`, http.StatusUnauthorized)
					return
				}
				actor = a
			}

			next.ServeHTTP(w, r.WithContext(WithActor(r.Context(), actor)))
		})
	}
}

func actorFromDebugHeaders(r *http.Request) *Actor {
	t := r.Header.Get("X-Debug-Actor-Type")
	id := r.Header.Get("X-Debug-Actor-Id")
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

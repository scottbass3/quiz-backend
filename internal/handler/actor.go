package handler

import (
	"net/http"

	"github.com/scottbass3/quizz-backend/internal/auth"
	"github.com/scottbass3/quizz-backend/internal/domain"
)

// devActor holds the resolved actor identity for use within a handler.
type devActor struct {
	Type domain.ActorType
	ID   string
}

// extractActor reads the Actor set by the auth middleware from the request context.
// The middleware populates it from an OIDC session cookie (OIDC_ENABLED=true)
// or from X-Debug-Actor-* headers (dev mode).
func extractActor(r *http.Request) devActor {
	a := auth.ActorFromContext(r.Context())
	if a == nil {
		return devActor{Type: domain.ActorTypeUser, ID: "anonymous"}
	}
	return devActor{Type: a.ActorType, ID: a.Sub}
}

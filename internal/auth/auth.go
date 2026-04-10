package auth

import (
	"context"

	"github.com/scottbass3/quizz-backend/internal/domain"
)

type contextKey int

const actorKey contextKey = 1

// Actor represents an authenticated principal, populated either from an OIDC
// session cookie (production) or from X-Debug-Actor-* headers (dev mode).
type Actor struct {
	Sub       string
	Name      string
	Email     string
	ActorType domain.ActorType
}

func WithActor(ctx context.Context, a *Actor) context.Context {
	return context.WithValue(ctx, actorKey, a)
}

func ActorFromContext(ctx context.Context) *Actor {
	a, _ := ctx.Value(actorKey).(*Actor)
	return a
}

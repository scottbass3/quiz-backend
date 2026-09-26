package handler

import (
	"net/http"

	"github.com/scottbass3/quizz-backend/internal/auth"
	"github.com/scottbass3/quizz-backend/internal/domain"
	"github.com/scottbass3/quizz-backend/internal/store"
)

// devActor holds the resolved actor identity for use within a handler.
type devActor struct {
	Type domain.ActorType
	ID   string
}

// canReadList reports whether a may read list l: public lists are readable by
// everyone, private lists only by their owner.
func canReadList(a devActor, l *store.QuestionListRecord) bool {
	return l.Visibility != string(domain.ListVisibilityPrivate) || l.OwnerID == a.ID
}

// canEditList reports whether a may modify list l (questions, custom themes):
// any admin for a public list, only the owner for a private list.
func canEditList(a devActor, l *store.QuestionListRecord) bool {
	if l.Visibility == string(domain.ListVisibilityPublic) {
		return a.Type == domain.ActorTypeAdmin
	}
	return l.OwnerID == a.ID
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

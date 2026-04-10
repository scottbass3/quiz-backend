package handler

import (
	"net/http"

	"github.com/scottbass3/quizz-backend/internal/domain"
)

// devActor holds the simulated identity extracted from dev-only debug headers.
// This is NOT secure — replace with real auth middleware before production.
type devActor struct {
	Type domain.ActorType
	ID   string
}

// extractActor reads the temporary identity simulation headers.
//
//	X-Debug-Actor-Type: admin | user  (default: "user")
//	X-Debug-Actor-Id:   <string>      (default: "anonymous")
func extractActor(r *http.Request) devActor {
	t := r.Header.Get("X-Debug-Actor-Type")
	id := r.Header.Get("X-Debug-Actor-Id")
	if t != "admin" && t != "user" {
		t = "user"
	}
	if id == "" {
		id = "anonymous"
	}
	return devActor{Type: domain.ActorType(t), ID: id}
}

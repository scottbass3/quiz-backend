package handler

import (
	"log/slog"
	"net/http"

	"github.com/scottbass3/quizz-backend/internal/auth"
)

// AuthHandler serves OIDC login, callback, logout, and session info endpoints.
type AuthHandler struct {
	provider      *auth.OIDCProvider // nil when OIDC is disabled
	sessionSecret []byte
	frontendURL   string
	oidcEnabled   bool
	logger        *slog.Logger
}

func NewAuthHandler(
	provider *auth.OIDCProvider,
	sessionSecret []byte,
	frontendURL string,
	oidcEnabled bool,
	logger *slog.Logger,
) *AuthHandler {
	return &AuthHandler{
		provider:      provider,
		sessionSecret: sessionSecret,
		frontendURL:   frontendURL,
		oidcEnabled:   oidcEnabled,
		logger:        logger,
	}
}

// GET /auth/login — redirect browser to the OIDC provider.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	if !h.oidcEnabled {
		writeError(w, http.StatusNotFound, "OIDC not enabled")
		return
	}

	state, err := auth.RandomString(16)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate state")
		return
	}
	nonce, err := auth.RandomString(16)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate nonce")
		return
	}

	stateToken, err := auth.NewStateToken(h.sessionSecret, state, nonce)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sign state")
		return
	}

	// Short-lived cookie scoped to /auth to carry state+nonce across the redirect.
	http.SetCookie(w, &http.Cookie{
		Name:     "oauth2_state",
		Value:    stateToken,
		Path:     "/auth",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})

	http.Redirect(w, r, h.provider.AuthCodeURL(state, nonce), http.StatusFound)
}

// GET /auth/callback — OIDC redirect target: exchange code, issue session cookie.
func (h *AuthHandler) Callback(w http.ResponseWriter, r *http.Request) {
	if !h.oidcEnabled {
		writeError(w, http.StatusNotFound, "OIDC not enabled")
		return
	}

	stateCookie, err := r.Cookie("oauth2_state")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing state cookie")
		return
	}
	savedState, savedNonce, err := auth.ParseStateToken(h.sessionSecret, stateCookie.Value)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid or expired state")
		return
	}
	if r.URL.Query().Get("state") != savedState {
		writeError(w, http.StatusBadRequest, "state mismatch")
		return
	}

	// Clear the state cookie.
	http.SetCookie(w, &http.Cookie{
		Name: "oauth2_state", Value: "", Path: "/auth", MaxAge: -1,
	})

	actor, err := h.provider.Exchange(r.Context(), r.URL.Query().Get("code"), savedNonce)
	if err != nil {
		h.logger.Error("oidc callback exchange", "error", err)
		writeError(w, http.StatusBadRequest, "authentication failed")
		return
	}

	token, err := auth.NewSessionToken(h.sessionSecret, actor)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	// Secure=false for local dev (no TLS). Set to true in production.
	auth.SetSessionCookie(w, token, false)

	http.Redirect(w, r, h.frontendURL, http.StatusFound)
}

// POST /auth/logout — clear the session cookie.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	auth.ClearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

// GET /auth/me — return the current actor from context (set by auth middleware).
// Returns oidc_enabled so the UI can adjust its controls accordingly.
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	a := auth.ActorFromContext(r.Context())
	if a == nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sub":          a.Sub,
		"name":         a.Name,
		"email":        a.Email,
		"actor_type":   a.ActorType,
		"oidc_enabled": h.oidcEnabled,
	})
}

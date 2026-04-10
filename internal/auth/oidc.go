package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/scottbass3/quizz-backend/internal/domain"
)

// OIDCProvider wraps the go-oidc provider and OAuth2 config.
type OIDCProvider struct {
	verifier     *gooidc.IDTokenVerifier
	oauth2Config oauth2.Config
	roleClaim    string
	adminRole    string
}

// NewOIDCProvider performs OIDC provider discovery and returns a configured OIDCProvider.
func NewOIDCProvider(
	ctx context.Context,
	issuerURL, clientID, clientSecret, redirectURL,
	roleClaim, adminRole string,
) (*OIDCProvider, error) {
	p, err := gooidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc: provider discovery for %q: %w", issuerURL, err)
	}
	return &OIDCProvider{
		verifier: p.Verifier(&gooidc.Config{ClientID: clientID}),
		oauth2Config: oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURL,
			Endpoint:     p.Endpoint(),
			Scopes:       []string{gooidc.ScopeOpenID, "profile", "email"},
		},
		roleClaim: roleClaim,
		adminRole: adminRole,
	}, nil
}

// AuthCodeURL returns the URL to redirect the user to for login.
func (p *OIDCProvider) AuthCodeURL(state, nonce string) string {
	return p.oauth2Config.AuthCodeURL(state, gooidc.Nonce(nonce))
}

// Exchange exchanges the authorization code for an Actor.
// It verifies the ID token and checks the nonce to prevent replay attacks.
func (p *OIDCProvider) Exchange(ctx context.Context, code, nonce string) (*Actor, error) {
	token, err := p.oauth2Config.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("oidc: code exchange: %w", err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return nil, errors.New("oidc: no id_token in response")
	}

	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("oidc: verify id_token: %w", err)
	}
	if idToken.Nonce != nonce {
		return nil, errors.New("oidc: nonce mismatch")
	}

	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("oidc: extract claims: %w", err)
	}

	name, _ := claims["name"].(string)
	email, _ := claims["email"].(string)

	return &Actor{
		Sub:       idToken.Subject,
		Name:      name,
		Email:     email,
		ActorType: p.actorTypeFromClaims(claims),
	}, nil
}

// actorTypeFromClaims derives the ActorType from a configurable role claim.
// Handles both scalar string and array-of-strings claim values.
func (p *OIDCProvider) actorTypeFromClaims(claims map[string]any) domain.ActorType {
	if p.roleClaim == "" || p.adminRole == "" {
		return domain.ActorTypeUser
	}
	v := claims[p.roleClaim]
	if role, ok := v.(string); ok && role == p.adminRole {
		return domain.ActorTypeAdmin
	}
	if roles, ok := v.([]any); ok {
		for _, r := range roles {
			if s, ok := r.(string); ok && s == p.adminRole {
				return domain.ActorTypeAdmin
			}
		}
	}
	return domain.ActorTypeUser
}

// RandomString returns a URL-safe base64-encoded random string of n bytes.
func RandomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

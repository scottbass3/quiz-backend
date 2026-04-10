package auth

import (
	"errors"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/scottbass3/quizz-backend/internal/domain"
)

const SessionCookie = "quizz_session"
const sessionTTL = 24 * time.Hour

type sessionClaims struct {
	Sub       string           `json:"sub"`
	Name      string           `json:"name"`
	Email     string           `json:"email"`
	ActorType domain.ActorType `json:"actor_type"`
	jwt.RegisteredClaims
}

func NewSessionToken(secret []byte, a *Actor) (string, error) {
	claims := sessionClaims{
		Sub:       a.Sub,
		Name:      a.Name,
		Email:     a.Email,
		ActorType: a.ActorType,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(sessionTTL)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}

func ParseSessionToken(secret []byte, tokenStr string) (*Actor, error) {
	var claims sessionClaims
	token, err := jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return secret, nil
	})
	if err != nil || !token.Valid {
		return nil, errors.New("invalid or expired session")
	}
	return &Actor{
		Sub:       claims.Sub,
		Name:      claims.Name,
		Email:     claims.Email,
		ActorType: claims.ActorType,
	}, nil
}

// stateClaims holds the OAuth2 state and OIDC nonce for CSRF protection.
type stateClaims struct {
	State string `json:"state"`
	Nonce string `json:"nonce"`
	jwt.RegisteredClaims
}

func NewStateToken(secret []byte, state, nonce string) (string, error) {
	claims := stateClaims{
		State: state,
		Nonce: nonce,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(10 * time.Minute)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}

func ParseStateToken(secret []byte, tokenStr string) (state, nonce string, err error) {
	var claims stateClaims
	token, err := jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return secret, nil
	})
	if err != nil || !token.Valid {
		return "", "", errors.New("invalid or expired state token")
	}
	return claims.State, claims.Nonce, nil
}

func SetSessionCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

package config

import (
	"log/slog"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr         string
	DatabaseURL      string
	RedisAddr        string
	RedisPassword    string
	LogLevel         slog.Level
	ShutdownTimeout  time.Duration
	GameInitialLives int

	// OIDC authentication. Set OIDC_ENABLED=true to activate.
	// When disabled, identity is simulated via X-Debug-Actor-* headers (dev only).
	OIDCEnabled      bool
	OIDCIssuerURL    string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string  // where the OIDC provider posts the code back
	OIDCRoleClaim    string  // claim containing the user's role (default: "role")
	OIDCAdminRole    string  // role value that maps to ActorTypeAdmin (default: "admin")
	OIDCFrontendURL  string  // redirect destination after successful login

	// SessionSecret is the HMAC key used to sign session JWTs and OAuth2 state cookies.
	// Must be set to a random secret in production.
	SessionSecret string
}

func Load() *Config {
	return &Config{
		HTTPAddr:         getenv("HTTP_ADDR", ":8080"),
		DatabaseURL:      getenv("DATABASE_URL", "postgres://quizz:quizz@localhost:5432/quizz?sslmode=disable"),
		RedisAddr:        getenv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:    getenv("REDIS_PASSWORD", ""),
		LogLevel:         parseLogLevel(getenv("LOG_LEVEL", "info")),
		ShutdownTimeout:  parseDuration(getenv("SHUTDOWN_TIMEOUT", "10s"), 10*time.Second),
		GameInitialLives: parseInt(getenv("GAME_INITIAL_LIVES", "3"), 3),

		OIDCEnabled:      parseBool(getenv("OIDC_ENABLED", "false")),
		OIDCIssuerURL:    getenv("OIDC_ISSUER_URL", ""),
		OIDCClientID:     getenv("OIDC_CLIENT_ID", ""),
		OIDCClientSecret: getenv("OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURL:  getenv("OIDC_REDIRECT_URL", "http://localhost:8080/auth/callback"),
		OIDCRoleClaim:    getenv("OIDC_ROLE_CLAIM", "role"),
		OIDCAdminRole:    getenv("OIDC_ADMIN_ROLE", "admin"),
		OIDCFrontendURL:  getenv("OIDC_FRONTEND_URL", "http://localhost:5173"),

		SessionSecret: getenv("SESSION_SECRET", "dev-secret-change-in-production"),
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseLogLevel(s string) slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return slog.LevelInfo
	}
	return l
}

func parseDuration(s string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}

func parseInt(s string, fallback int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}

func parseBool(s string) bool {
	b, _ := strconv.ParseBool(s)
	return b
}

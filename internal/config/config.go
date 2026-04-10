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

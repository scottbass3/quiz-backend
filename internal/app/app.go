package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/scottbass3/quizz-backend/internal/config"
	"github.com/scottbass3/quizz-backend/internal/game"
	"github.com/scottbass3/quizz-backend/internal/handler"
	"github.com/scottbass3/quizz-backend/internal/postgres"
	appredis "github.com/scottbass3/quizz-backend/internal/redis"
	"github.com/scottbass3/quizz-backend/internal/store"
	appws "github.com/scottbass3/quizz-backend/internal/ws"
	"github.com/scottbass3/quizz-backend/migrations"
)

// hubStore is a simple in-process registry of per-game WebSocket hubs.
type hubStore struct {
	mu     sync.RWMutex
	hubs   map[string]*appws.Hub
	logger *slog.Logger
}

func newHubStore(logger *slog.Logger) *hubStore {
	return &hubStore{
		hubs:   make(map[string]*appws.Hub),
		logger: logger,
	}
}

func (s *hubStore) GetOrCreate(gameID string) *appws.Hub {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.hubs[gameID]; ok {
		return h
	}
	h := appws.NewHub(s.logger)
	s.hubs[gameID] = h
	return h
}

func (s *hubStore) Get(gameID string) (*appws.Hub, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.hubs[gameID]
	return h, ok
}

// App is the top-level application that wires all dependencies together.
type App struct {
	cfg    *config.Config
	logger *slog.Logger
	server *http.Server
	pg     *postgres.DB
	redis  *appredis.Client
}

func New(cfg *config.Config, logger *slog.Logger) (*App, error) {
	a := &App{cfg: cfg, logger: logger}

	// Postgres
	pg, err := postgres.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("app: postgres: %w", err)
	}
	a.pg = pg

	if err := pg.RunMigrations(context.Background(), migrations.SQL); err != nil {
		return nil, fmt.Errorf("app: migrations: %w", err)
	}
	logger.Info("migrations applied")

	// Redis
	rdb := appredis.New(cfg.RedisAddr, cfg.RedisPassword)
	if err := rdb.Ping(context.Background()); err != nil {
		return nil, fmt.Errorf("app: redis: %w", err)
	}
	a.redis = rdb

	// Game layer
	manager := game.NewManager(game.EngineConfig{
		InitialLives: cfg.GameInitialLives,
	})
	hubs := newHubStore(logger)

	// Handlers
	var gs store.GameStore = pg
	var ps store.PlayerStore = pg
	gameH := handler.NewGameHandler(manager, hubs, gs, ps, logger)
	healthH := handler.NewHealthHandler()

	// Router
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(requestLogger(logger))

	r.Get("/health", healthH.Health)

	r.Route("/games", func(r chi.Router) {
		r.Post("/", gameH.CreateGame)
		r.Get("/{id}", gameH.GetGame)
		r.Post("/{id}/join", gameH.JoinGame)
		r.Post("/{id}/questions", gameH.AddQuestion)
		r.Post("/{id}/start", gameH.StartNextQuestion)
		r.Post("/{id}/close", gameH.CloseQuestion)
	})

	r.Get("/ws", gameH.WebSocket)

	a.server = &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // disabled for WebSocket connections
		IdleTimeout:  60 * time.Second,
	}

	return a, nil
}

func (a *App) Run(ctx context.Context) error {
	errCh := make(chan error, 1)

	go func() {
		a.logger.Info("server starting", "addr", a.cfg.HTTPAddr)
		if err := a.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		a.logger.Info("shutting down...")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownTimeout)
	defer cancel()

	if err := a.server.Shutdown(shutdownCtx); err != nil {
		a.logger.Error("graceful shutdown failed", "error", err)
	}

	a.pg.Close()
	if err := a.redis.Close(); err != nil {
		a.logger.Error("redis close", "error", err)
	}

	a.logger.Info("shutdown complete")
	return nil
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			next.ServeHTTP(ww, r)
			logger.Info("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"duration", time.Since(start),
				"request_id", middleware.GetReqID(r.Context()),
			)
		})
	}
}

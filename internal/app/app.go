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

// gameSession holds the per-game WebSocket hub and the Redis pub/sub broadcaster.
type gameSession struct {
	hub         *appws.Hub
	broadcaster *appredis.PubSubBroadcaster
}

// gameSessionStore manages per-game sessions (hub + Redis broadcaster).
// It implements handler.gameSessionRegistry.
type gameSessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*gameSession
	rdb      *appredis.Client
	logger   *slog.Logger
}

func newGameSessionStore(rdb *appredis.Client, logger *slog.Logger) *gameSessionStore {
	return &gameSessionStore{
		sessions: make(map[string]*gameSession),
		rdb:      rdb,
		logger:   logger,
	}
}

// GetOrCreate returns the broadcaster (for the engine) and the hub (for WS registration).
// If no session exists for gameID, both are created and the Redis subscriber is started.
func (s *gameSessionStore) GetOrCreate(gameID string) (game.Broadcaster, *appws.Hub) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sess, ok := s.sessions[gameID]; ok {
		return sess.broadcaster, sess.hub
	}

	hub := appws.NewHub(s.logger)
	b := appredis.NewPubSubBroadcaster(s.rdb.Unwrap(), gameID, hub, s.logger)

	s.sessions[gameID] = &gameSession{hub: hub, broadcaster: b}
	s.logger.Debug("game session created", "game_id", gameID)
	return b, hub
}

// GetHub returns the WS hub for an existing game (used by the WebSocket upgrade handler).
func (s *gameSessionStore) GetHub(gameID string) (*appws.Hub, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sess, ok := s.sessions[gameID]; ok {
		return sess.hub, true
	}
	return nil, false
}

// Stop cancels all active Redis subscriptions. Called at shutdown.
func (s *gameSessionStore) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		sess.broadcaster.Stop()
	}
	s.logger.Debug("game session store stopped", "count", len(s.sessions))
}

// App is the top-level application that wires all dependencies together.
type App struct {
	cfg      *config.Config
	logger   *slog.Logger
	server   *http.Server
	pg       *postgres.DB
	redis    *appredis.Client
	sessions *gameSessionStore
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
	logger.Info("redis connected", "addr", cfg.RedisAddr)

	// Game session store: manages hub + Redis pub/sub broadcaster per game.
	sessions := newGameSessionStore(rdb, logger)
	a.sessions = sessions

	// Game layer
	engineCfg := game.EngineConfig{InitialLives: cfg.GameInitialLives}
	manager := game.NewManager(engineCfg)

	// Stores
	var gs store.GameStore = pg
	var ps store.PlayerStore = pg
	var qls store.QuestionListStore = pg

	// Handlers
	gameH := handler.NewGameHandler(manager, sessions, gs, ps, qls, engineCfg, logger)
	qlH := handler.NewQuestionListHandler(qls, logger)
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
		r.Post("/{id}/start", gameH.StartNextQuestion)
		r.Post("/{id}/close", gameH.CloseQuestion)
	})

	r.Route("/question-lists", func(r chi.Router) {
		r.Post("/", qlH.Create)
		r.Get("/public", qlH.ListPublic)
		r.Get("/private", qlH.ListPrivate)
		r.Get("/{id}", qlH.Get)
		r.Get("/{id}/questions", qlH.ListQuestions)
		r.Post("/{id}/questions", qlH.AddQuestion)
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

	a.sessions.Stop()
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

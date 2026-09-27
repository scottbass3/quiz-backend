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
	"github.com/scottbass3/quizz-backend/internal/auth"
	"github.com/scottbass3/quizz-backend/internal/config"
	"github.com/scottbass3/quizz-backend/internal/game"
	"github.com/scottbass3/quizz-backend/internal/handler"
	"github.com/scottbass3/quizz-backend/internal/postgres"
	appredis "github.com/scottbass3/quizz-backend/internal/redis"
	"github.com/scottbass3/quizz-backend/internal/store"
	appws "github.com/scottbass3/quizz-backend/internal/ws"
	"github.com/scottbass3/quizz-backend/migrations"
)

// gameSession is this instance's local fan-out for one game: a WebSocket hub
// and the Redis subscription feeding it. It exists only while local
// WebSocket clients of that game are connected (refs > 0).
type gameSession struct {
	hub  *appws.Hub
	stop func() // cancels the Redis subscription
	refs int
}

// gameSessionStore manages the local sessions of this instance. Game state
// and event publishing do not depend on it: any instance can serve any game,
// and events reach every instance with connected players through Redis.
// It implements handler.gameSessionRegistry.
type gameSessionStore struct {
	mu       sync.Mutex
	sessions map[string]*gameSession
	rdb      *appredis.Client
	pub      *appredis.Publisher
	logger   *slog.Logger
}

func newGameSessionStore(rdb *appredis.Client, logger *slog.Logger) *gameSessionStore {
	return &gameSessionStore{
		sessions: make(map[string]*gameSession),
		rdb:      rdb,
		pub:      appredis.NewPublisher(rdb.Unwrap(), logger),
		logger:   logger,
	}
}

// Broadcaster returns the publisher of a game's events (used by the engine).
func (s *gameSessionStore) Broadcaster(gameID string) game.Broadcaster {
	return s.pub.For(gameID)
}

// Acquire returns the local hub of gameID for a new WebSocket client,
// subscribing to the game's events on first use. Every successful Acquire
// must be paired with a Release.
func (s *gameSessionStore) Acquire(gameID string) (*appws.Hub, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sess, ok := s.sessions[gameID]; ok {
		sess.refs++
		return sess.hub, nil
	}
	hub := appws.NewHub(s.logger)
	stop, err := appredis.Subscribe(s.rdb.Unwrap(), gameID, hub, s.logger)
	if err != nil {
		return nil, fmt.Errorf("subscribe to game %s: %w", gameID, err)
	}
	s.sessions[gameID] = &gameSession{hub: hub, stop: stop, refs: 1}
	s.logger.Debug("game session created", "game_id", gameID)
	return hub, nil
}

// Release drops a client's reference; the last one unsubscribes.
func (s *gameSessionStore) Release(gameID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[gameID]
	if !ok {
		return
	}
	if sess.refs--; sess.refs > 0 {
		return
	}
	sess.stop()
	delete(s.sessions, gameID)
	s.logger.Debug("game session removed", "game_id", gameID)
}

// Remove closes the WebSocket connections of a game that no longer exists.
// The clients' handlers then Release the session.
func (s *gameSessionStore) Remove(gameID string) {
	s.mu.Lock()
	sess, ok := s.sessions[gameID]
	s.mu.Unlock()
	if ok {
		sess.hub.CloseAll()
	}
}

// GameIDs lists the games with local WebSocket clients.
func (s *gameSessionStore) GameIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	return ids
}

// Stop cancels all active Redis subscriptions. Called at shutdown.
func (s *gameSessionStore) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		sess.stop()
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
	manager  *game.Manager
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

	// Game layer: default config; individual games may override it via POST /games.
	defaultEngineCfg := game.EngineConfig{InitialLives: cfg.GameInitialLives}
	manager := game.NewManager()
	a.manager = manager

	// Stores
	var gs store.GameStore = pg
	var ps store.PlayerStore = pg
	var qls store.QuestionListStore = pg
	var ts store.ThemeStore = pg

	// ── Auth ────────────────────────────────────────────────────────────────────

	sessionSecret := []byte(cfg.SessionSecret)
	authMW := auth.Middleware(sessionSecret, cfg.OIDCEnabled)

	var oidcProvider *auth.OIDCProvider
	if cfg.OIDCEnabled {
		p, err := auth.NewOIDCProvider(
			context.Background(),
			cfg.OIDCIssuerURL,
			cfg.OIDCClientID,
			cfg.OIDCClientSecret,
			cfg.OIDCRedirectURL,
			cfg.OIDCRoleClaim,
			cfg.OIDCAdminRole,
		)
		if err != nil {
			return nil, fmt.Errorf("app: oidc: %w", err)
		}
		oidcProvider = p
		logger.Info("OIDC enabled", "issuer", cfg.OIDCIssuerURL)
	} else {
		logger.Info("OIDC disabled — using X-Debug-Actor-* headers")
	}

	// ── Handlers ────────────────────────────────────────────────────────────────

	gameH := handler.NewGameHandler(manager, sessions, gs, ps, qls, defaultEngineCfg, logger)
	allowedOrigins := parseOrigins(cfg.CORSAllowedOrigins)
	if len(allowedOrigins) > 0 {
		gameH.RestrictOrigins(allowedOrigins)
		logger.Info("CORS enabled", "origins", allowedOrigins)
	}
	qlH := handler.NewQuestionListHandler(qls, ts, logger)
	themeH := handler.NewThemeHandler(ts, qls, logger)
	healthH := handler.NewHealthHandler()
	authH := handler.NewAuthHandler(oidcProvider, sessionSecret, cfg.OIDCFrontendURL, cfg.OIDCEnabled, logger)

	// ── Router ──────────────────────────────────────────────────────────────────

	r := chi.NewRouter()
	r.Use(corsMiddleware(allowedOrigins)) // first: preflights carry no credentials
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(requestLogger(logger))

	// Public routes — no auth required.
	r.Get("/health", healthH.Health)

	r.Route("/auth", func(r chi.Router) {
		r.Get("/login", authH.Login)
		r.Get("/callback", authH.Callback)
		r.Post("/logout", authH.Logout)
		// /auth/me requires the actor to be set in context.
		r.With(authMW).Get("/me", authH.Me)
	})

	// Protected routes — auth middleware required.
	r.Group(func(r chi.Router) {
		r.Use(authMW)

		r.Route("/games", func(r chi.Router) {
			r.Post("/", gameH.CreateGame)
			r.Get("/", gameH.ListMyGames)
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
			r.Put("/{id}", qlH.UpdateList)
			r.Delete("/{id}", qlH.DeleteList)
			r.Put("/{id}/questions/order", qlH.ReorderQuestions)
			r.Put("/{id}/questions/{questionID}", qlH.UpdateQuestion)
			r.Delete("/{id}/questions/{questionID}", qlH.DeleteQuestion)

			// Custom themes of a list.
			r.Get("/{id}/themes", themeH.ListForList)
			r.Post("/{id}/themes", themeH.CreateForList)
			r.Get("/{id}/themes/{themeID}", themeH.GetForList)
			r.Put("/{id}/themes/{themeID}", themeH.UpdateForList)
			r.Delete("/{id}/themes/{themeID}", themeH.DeleteForList)
		})

		// Global themes (write: admin only).
		r.Route("/themes", func(r chi.Router) {
			r.Get("/", themeH.ListGlobal)
			r.Post("/", themeH.CreateGlobal)
			r.Get("/{themeID}", themeH.GetGlobal)
			r.Put("/{themeID}", themeH.UpdateGlobal)
			r.Delete("/{themeID}", themeH.DeleteGlobal)
		})

		r.Get("/ws", gameH.WebSocket)
	})

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

	go a.sweepGames(ctx)

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

// sweepInterval is how often expired games are evicted from memory.
const sweepInterval = time.Minute

// sweepGames periodically evicts finished and idle games from memory and
// releases their Redis subscription and WebSocket connections.
func (a *App) sweepGames(ctx context.Context) {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			removed := a.manager.Sweep(now, a.cfg.GameFinishedTTL, a.cfg.GameIdleTTL)
			for _, id := range removed {
				a.sessions.Remove(id)
			}
			if len(removed) > 0 {
				a.logger.Info("evicted games", "count", len(removed), "remaining", a.manager.Count())
			}
		}
	}
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

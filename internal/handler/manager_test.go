package handler

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/scottbass3/quizz-backend/internal/game"
	appredis "github.com/scottbass3/quizz-backend/internal/redis"
)

// testManager returns a game manager over a fresh in-process Redis.
// broadcaster delivers every game's events (a real hub, or a no-op).
func testManager(t *testing.T, broadcaster game.Broadcaster) *game.Manager {
	t.Helper()
	mr := miniredis.RunT(t)
	store := appredis.NewGameStateStore(goredis.NewClient(&goredis.Options{Addr: mr.Addr()}), 2*time.Hour, 10*time.Minute)
	return game.NewManager(store, func(string) game.Broadcaster { return broadcaster },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

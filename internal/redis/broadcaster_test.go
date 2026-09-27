package redis_test

import (
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/scottbass3/quizz-backend/internal/domain"
	appredis "github.com/scottbass3/quizz-backend/internal/redis"
)

type recorder struct {
	mu     sync.Mutex
	all    []domain.EventType
	direct map[string][]domain.EventType
}

func (r *recorder) Broadcast(e domain.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.all = append(r.all, e.Type)
}

func (r *recorder) BroadcastTo(pid string, e domain.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.direct[pid] = append(r.direct[pid], e.Type)
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.all)
	for _, d := range r.direct {
		n += len(d)
	}
	return n
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Events published by any instance reach every subscribed instance.
func TestPublishSubscribe(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	instanceA := &recorder{direct: map[string][]domain.EventType{}}
	instanceB := &recorder{direct: map[string][]domain.EventType{}}
	stopA, err := appredis.Subscribe(rdb, "g1", instanceA, logger)
	if err != nil {
		t.Fatal(err)
	}
	stopB, err := appredis.Subscribe(rdb, "g1", instanceB, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer stopB()

	pub := appredis.NewPublisher(rdb, logger).For("g1")
	pub.Broadcast(domain.Event{Type: domain.EventQuestionStarted})
	pub.BroadcastTo("p1", domain.Event{Type: domain.EventLifeLost})
	appredis.NewPublisher(rdb, logger).For("other-game").Broadcast(domain.Event{Type: domain.EventGameOver})

	waitFor(t, func() bool { return instanceA.count() == 2 && instanceB.count() == 2 })
	if instanceA.all[0] != domain.EventQuestionStarted || instanceA.direct["p1"][0] != domain.EventLifeLost {
		t.Fatalf("unexpected events %+v", instanceA)
	}

	stopA()
	time.Sleep(50 * time.Millisecond)
	pub.Broadcast(domain.Event{Type: domain.EventGameOver})
	waitFor(t, func() bool { return instanceB.count() == 3 })
	if instanceA.count() != 2 {
		t.Fatal("a stopped subscription must not receive events")
	}
}

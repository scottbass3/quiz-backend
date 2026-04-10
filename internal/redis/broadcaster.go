package redis

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/redis/go-redis/v9"
	"github.com/scottbass3/quizz-backend/internal/domain"
)

// LocalForwarder is implemented by ws.Hub.
// Defined here to avoid an import cycle between the redis and ws packages.
type LocalForwarder interface {
	Broadcast(event domain.Event)
	BroadcastTo(playerID string, event domain.Event)
}

// pubsubMsg is the envelope published on the Redis channel.
type pubsubMsg struct {
	Target string       `json:"t"` // "*" = all players, anything else = specific playerID
	Event  domain.Event `json:"e"`
}

// PubSubBroadcaster implements game.Broadcaster via Redis pub/sub.
//
// On Broadcast / BroadcastTo: publishes the event to a per-game Redis channel.
// A subscriber goroutine reads from that channel and forwards to the local ws.Hub.
//
// This makes the game engine horizontally scalable: multiple API instances can
// subscribe to the same channel and each forward events to their own WS clients.
type PubSubBroadcaster struct {
	rdb    *redis.Client
	ch     string // Redis channel: "game:<gameID>:events"
	fwd    LocalForwarder
	logger *slog.Logger
	cancel context.CancelFunc
}

// NewPubSubBroadcaster creates a broadcaster and starts the subscriber goroutine.
// Call Stop() when the game ends to release the Redis subscription.
func NewPubSubBroadcaster(rdb *redis.Client, gameID string, fwd LocalForwarder, logger *slog.Logger) *PubSubBroadcaster {
	ctx, cancel := context.WithCancel(context.Background())
	b := &PubSubBroadcaster{
		rdb:    rdb,
		ch:     "game:" + gameID + ":events",
		fwd:    fwd,
		logger: logger,
		cancel: cancel,
	}
	go b.subscribe(ctx)
	return b
}

// Broadcast sends an event to all players in this game.
func (b *PubSubBroadcaster) Broadcast(event domain.Event) {
	b.publish("*", event)
}

// BroadcastTo sends an event to a single player.
func (b *PubSubBroadcaster) BroadcastTo(playerID string, event domain.Event) {
	b.publish(playerID, event)
}

// Stop cancels the subscriber goroutine and releases the Redis subscription.
func (b *PubSubBroadcaster) Stop() {
	b.cancel()
}

func (b *PubSubBroadcaster) publish(target string, event domain.Event) {
	msg := pubsubMsg{Target: target, Event: event}
	data, err := json.Marshal(msg)
	if err != nil {
		b.logger.Error("redis broadcaster: marshal event", "error", err)
		return
	}
	if err := b.rdb.Publish(context.Background(), b.ch, data).Err(); err != nil {
		b.logger.Error("redis broadcaster: publish", "channel", b.ch, "error", err)
	}
}

func (b *PubSubBroadcaster) subscribe(ctx context.Context) {
	sub := b.rdb.Subscribe(ctx, b.ch)
	defer func() {
		if err := sub.Close(); err != nil {
			b.logger.Debug("redis broadcaster: subscription closed", "error", err)
		}
	}()

	msgCh := sub.Channel()
	b.logger.Debug("redis broadcaster: subscribed", "channel", b.ch)

	for {
		select {
		case msg, ok := <-msgCh:
			if !ok {
				return
			}
			var m pubsubMsg
			if err := json.Unmarshal([]byte(msg.Payload), &m); err != nil {
				b.logger.Warn("redis broadcaster: unmarshal", "error", err, "payload", msg.Payload)
				continue
			}
			if m.Target == "*" {
				b.fwd.Broadcast(m.Event)
			} else {
				b.fwd.BroadcastTo(m.Target, m.Event)
			}
		case <-ctx.Done():
			return
		}
	}
}

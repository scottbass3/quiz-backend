package redis

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/scottbass3/quizz-backend/internal/domain"
)

// LocalForwarder is implemented by ws.Hub.
// Defined here to avoid an import cycle between the redis and ws packages.
type LocalForwarder interface {
	Broadcast(event domain.Event)
	BroadcastTo(playerID string, event domain.Event)
	// Claim closes the local connection of playerID if it is older than seq.
	Claim(playerID string, seq int64)
}

// pubsubMsg is the envelope published on the Redis channel: either an event
// for players, or a connection claim for the instances.
type pubsubMsg struct {
	Target string       `json:"t"` // "*" = all players, anything else = specific playerID
	Event  domain.Event `json:"e"`
	Claim  *claimMsg    `json:"c,omitempty"`
}

// claimMsg announces generation Seq of PlayerID's WebSocket connection.
type claimMsg struct {
	PlayerID string `json:"p"`
	Seq      int64  `json:"s"`
}

func connectionsKey(gameID string) string { return "game:{" + gameID + "}:conns" }

func channel(gameID string) string {
	return "game:" + gameID + ":events"
}

// Publisher publishes game events on Redis pub/sub. It holds no
// subscription, so any instance can publish events for any game; every
// instance with players of that game connected forwards them (see Subscribe).
type Publisher struct {
	rdb    *redis.Client
	logger *slog.Logger
}

func NewPublisher(rdb *redis.Client, logger *slog.Logger) *Publisher {
	return &Publisher{rdb: rdb, logger: logger}
}

// ClaimConnection gives a new WebSocket connection of playerID its
// generation (a per-player counter in Redis) and announces it to every
// instance subscribed to the game, which then close any older connection of
// that player. A player therefore has one live connection across all
// instances, whichever instance a load balancer picks. ttl bounds the life of
// the counter.
func (p *Publisher) ClaimConnection(ctx context.Context, gameID, playerID string, ttl time.Duration) (int64, error) {
	key := connectionsKey(gameID)
	var seqCmd *redis.IntCmd
	if _, err := p.rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		seqCmd = pipe.HIncrBy(ctx, key, playerID, 1)
		pipe.Expire(ctx, key, ttl)
		return nil
	}); err != nil {
		return 0, err
	}
	seq := seqCmd.Val()
	data, err := json.Marshal(pubsubMsg{Claim: &claimMsg{PlayerID: playerID, Seq: seq}})
	if err != nil {
		return 0, err
	}
	if err := p.rdb.Publish(ctx, channel(gameID), data).Err(); err != nil {
		return 0, err
	}
	return seq, nil
}

// For returns a broadcaster (game.Broadcaster) for one game.
func (p *Publisher) For(gameID string) *GamePublisher {
	return &GamePublisher{p: p, ch: channel(gameID)}
}

// GamePublisher implements game.Broadcaster for one game.
type GamePublisher struct {
	p  *Publisher
	ch string
}

// Broadcast sends an event to all players in this game.
func (g *GamePublisher) Broadcast(event domain.Event) {
	g.publish("*", event)
}

// BroadcastTo sends an event to a single player.
func (g *GamePublisher) BroadcastTo(playerID string, event domain.Event) {
	g.publish(playerID, event)
}

func (g *GamePublisher) publish(target string, event domain.Event) {
	data, err := json.Marshal(pubsubMsg{Target: target, Event: event})
	if err != nil {
		g.p.logger.Error("redis publisher: marshal event", "error", err)
		return
	}
	if err := g.p.rdb.Publish(context.Background(), g.ch, data).Err(); err != nil {
		g.p.logger.Error("redis publisher: publish", "channel", g.ch, "error", err)
	}
}

// Subscribe forwards the events of gameID to fwd (the local ws.Hub) until
// the returned stop function is called. It returns once Redis has confirmed
// the subscription, so no event published afterwards is missed.
func Subscribe(rdb *redis.Client, gameID string, fwd LocalForwarder, logger *slog.Logger) (stop func(), err error) {
	ctx, cancel := context.WithCancel(context.Background())
	sub := rdb.Subscribe(ctx, channel(gameID))
	confirmCtx, confirmCancel := context.WithTimeout(ctx, 5*time.Second)
	defer confirmCancel()
	if _, err := sub.Receive(confirmCtx); err != nil {
		cancel()
		sub.Close()
		return nil, err
	}
	go func() {
		defer func() {
			if err := sub.Close(); err != nil {
				logger.Debug("redis subscriber: close", "error", err)
			}
		}()
		msgCh := sub.Channel()
		for {
			select {
			case msg, ok := <-msgCh:
				if !ok {
					return
				}
				var m pubsubMsg
				if err := json.Unmarshal([]byte(msg.Payload), &m); err != nil {
					logger.Warn("redis subscriber: unmarshal", "error", err, "payload", msg.Payload)
					continue
				}
				switch {
				case m.Claim != nil:
					fwd.Claim(m.Claim.PlayerID, m.Claim.Seq)
				case m.Target == "*":
					fwd.Broadcast(m.Event)
				default:
					fwd.BroadcastTo(m.Target, m.Event)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	logger.Debug("redis subscriber: subscribed", "channel", channel(gameID))
	return cancel, nil
}

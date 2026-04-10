package redis

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// Client wraps the go-redis client.
// For now it is a thin layer; pub/sub and distributed locking will be added later.
type Client struct {
	rdb *redis.Client
}

func New(addr, password string) *Client {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       0,
	})
	return &Client{rdb: rdb}
}

func (c *Client) Ping(ctx context.Context) error {
	if err := c.rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis: ping: %w", err)
	}
	return nil
}

func (c *Client) Close() error {
	return c.rdb.Close()
}

// Unwrap returns the underlying redis.Client for direct use when needed.
func (c *Client) Unwrap() *redis.Client {
	return c.rdb
}

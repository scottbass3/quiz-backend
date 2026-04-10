package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 4096
)

// IncomingMessage is the envelope for all client → server messages.
type IncomingMessage struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// SubmitAnswerData is the payload for type "submit_answer".
type SubmitAnswerData struct {
	QuestionID string `json:"question_id"`
	OptionID   string `json:"option_id"`
}

// MessageHandler is called for each valid message received from the client.
type MessageHandler func(playerID string, msg IncomingMessage)

// Client wraps a single WebSocket connection with a non-blocking send buffer.
type Client struct {
	conn     *websocket.Conn
	buf      chan []byte
	playerID string
	gameID   string
	logger   *slog.Logger
}

func NewClient(conn *websocket.Conn, playerID, gameID string, logger *slog.Logger) *Client {
	return &Client{
		conn:     conn,
		buf:      make(chan []byte, 256),
		playerID: playerID,
		gameID:   gameID,
		logger:   logger,
	}
}

// send queues a message; drops it if the buffer is full.
func (c *Client) send(msg []byte) {
	select {
	case c.buf <- msg:
	default:
		c.logger.Warn("ws: client send buffer full, dropping message", "player_id", c.playerID)
	}
}

// WritePump drains the send buffer to the WebSocket connection.
// It must run in its own goroutine.
func (c *Client) WritePump(ctx context.Context) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.buf:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}

		case <-ctx.Done():
			c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			return
		}
	}
}

// ReadPump reads messages from the WebSocket and calls handler.
// It must run in its own goroutine and blocks until the connection closes.
func (c *Client) ReadPump(ctx context.Context, handler MessageHandler) {
	defer c.conn.Close()

	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				c.logger.Warn("ws: unexpected close", "player_id", c.playerID, "error", err)
			}
			return
		}

		var msg IncomingMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			c.logger.Warn("ws: invalid message", "player_id", c.playerID, "error", err)
			continue
		}
		handler(c.playerID, msg)
	}
}

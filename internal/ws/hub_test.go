package ws

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// newTestClient returns a client of playerID with generation seq, backed by a
// real WebSocket, and a function reporting whether the peer saw it closed.
func newTestClient(t *testing.T, playerID string, seq int64) (*Client, func() bool) {
	t.Helper()
	serverConns := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		serverConns <- conn
	}))
	t.Cleanup(srv.Close)
	peer, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { peer.Close() })
	client := NewClient(<-serverConns, playerID, "g1", seq, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// A background reader notices when the server side closes the connection.
	// (A read that times out would leave the gorilla connection unusable, so
	// the peer is read without deadline.)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := peer.ReadMessage(); err != nil {
				return
			}
		}
	}()
	closed := func() bool {
		select {
		case <-done:
			return true
		case <-time.After(200 * time.Millisecond):
			return false
		}
	}
	return client, closed
}

func newTestHub() *Hub {
	return NewHub(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestRegister_NewerReplacesOlder(t *testing.T) {
	h := newTestHub()
	old, oldClosed := newTestClient(t, "p1", 1)
	cur, curClosed := newTestClient(t, "p1", 2)

	if !h.Register("p1", old) || !h.Register("p1", cur) {
		t.Fatal("newer connections must be accepted")
	}
	if !oldClosed() || curClosed() {
		t.Fatal("the older connection must be closed, the newer kept")
	}
}

func TestRegister_OlderIsRefused(t *testing.T) {
	h := newTestHub()
	cur, curClosed := newTestClient(t, "p1", 2)
	late, lateClosed := newTestClient(t, "p1", 1)

	h.Register("p1", cur)
	if h.Register("p1", late) {
		t.Fatal("an older connection must be refused")
	}
	if !lateClosed() || curClosed() {
		t.Fatal("the refused connection must be closed, the current one kept")
	}
}

func TestClaim_ClosesOlderLocalConnection(t *testing.T) {
	h := newTestHub()
	c, closed := newTestClient(t, "p1", 1)
	h.Register("p1", c)

	h.Claim("p1", 1) // own claim, echoed back: no effect
	if closed() {
		t.Fatal("a claim of the same generation must not close the connection")
	}
	h.Claim("p1", 2) // newer connection somewhere else
	if !closed() || h.ConnectedCount() != 0 {
		t.Fatal("a newer claim must remove and close the local connection")
	}
}

// A claim from another instance can arrive before the older local
// connection is registered: that connection must then be refused.
func TestClaim_BeforeRegister(t *testing.T) {
	h := newTestHub()
	h.Claim("p1", 5)
	late, lateClosed := newTestClient(t, "p1", 4)

	if h.Register("p1", late) || !lateClosed() {
		t.Fatal("a connection older than an announced one must be refused")
	}
	fresh, freshClosed := newTestClient(t, "p1", 6)
	if !h.Register("p1", fresh) || freshClosed() {
		t.Fatal("a newer connection must still be accepted")
	}
}

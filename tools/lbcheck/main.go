// lbcheck reconnects one player several times through a load balancer and
// checks that each previous connection is closed and that only the latest
// one receives events, whichever instance each connection lands on.
//
// Usage (with `make up-multi`): make lb-check, or
//
//	LB_ADDR=localhost:8090 go run ./tools/lbcheck
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

var lb = func() string {
	if v := os.Getenv("LB_ADDR"); v != "" {
		return v
	}
	return "localhost:8090"
}()

var base = "http://" + lb

func call(method, path, actorType, actor string, body any, out any) int {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, base+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Debug-Actor-Type", actorType)
	req.Header.Set("X-Debug-Actor-Id", actor)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func next(c *websocket.Conn, wait time.Duration) string {
	c.SetReadDeadline(time.Now().Add(wait))
	var ev struct{ Type string }
	if err := c.ReadJSON(&ev); err != nil {
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			return "timeout"
		}
		return "closed"
	}
	return ev.Type
}

func fail(format string, a ...any) {
	fmt.Printf("FAIL: "+format+"\n", a...)
	os.Exit(1)
}

func main() {
	var list struct{ ID string }
	call("POST", "/question-lists", "admin", "lb-admin", map[string]any{"name": "lb", "visibility": "public"}, &list)
	for i := 0; i < 2; i++ {
		call("POST", "/question-lists/"+list.ID+"/questions", "admin", "lb-admin", map[string]any{
			"text": "q", "options": []map[string]string{{"id": "a", "text": "1"}, {"id": "b", "text": "2"}}, "correct_option_id": "a"}, nil)
	}
	var game struct {
		GameID string `json:"game_id"`
	}
	call("POST", "/games", "user", "lb-host", map[string]any{"owner_name": "Host", "question_list_id": list.ID}, &game)
	var joined struct {
		PlayerID string `json:"player_id"`
	}
	call("POST", "/games/"+game.GameID+"/join", "user", "lb-bob", map[string]any{"player_name": "Bob"}, &joined)
	url := fmt.Sprintf("ws://%s/ws?gameId=%s&playerId=%s&debugActorId=lb-bob", lb, game.GameID, joined.PlayerID)

	var conns []*websocket.Conn
	for i := 0; i < 6; i++ {
		c, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			fail("dial %d: %v", i, err)
		}
		if ev := next(c, 2*time.Second); ev != "game_joined" {
			fail("connection %d: expected game_joined, got %s", i, ev)
		}
		if i > 0 {
			if ev := next(conns[i-1], 2*time.Second); ev != "closed" {
				fail("reconnect %d: previous connection still open (%s)", i, ev)
			}
		}
		conns = append(conns, c)
	}
	fmt.Println("6 reconnects through the load balancer: each previous connection was closed")

	if code := call("POST", "/games/"+game.GameID+"/start", "user", "lb-host", nil, nil); code != 200 {
		fail("start: %d", code)
	}
	received := 0
	for i, c := range conns {
		if ev := next(c, time.Second); ev == "question_started" {
			received++
			if i != len(conns)-1 {
				fail("an old connection (%d) received the event", i)
			}
		}
	}
	if received != 1 {
		fail("question_started delivered %d times, want 1", received)
	}
	fmt.Println("question_started delivered once, to the latest connection")
}

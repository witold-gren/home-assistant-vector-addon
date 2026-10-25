package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/robot"
)

type fakeRobot struct{ calls []string }

func (f *fakeRobot) ESN() string { return "00a1b2c3" }
func (f *fakeRobot) Snapshot() robot.State {
	return robot.State{ESN: "00a1b2c3", Name: "Vector", Connected: true}
}
func (f *fakeRobot) Command(_ context.Context, name string, args robot.Args) (any, error) {
	f.calls = append(f.calls, name)
	if name == "fail" {
		return nil, errors.New("nope")
	}
	return map[string]any{"echo": string(args["text"])}, nil
}

func setup(t *testing.T) (*Server, *httptest.Server) {
	srv := New("test", "secret", slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.AddRobot(&fakeRobot{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return srv, hs
}

func dial(t *testing.T, hs *httptest.Server, token string) (*websocket.Conn, error) {
	h := http.Header{}
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	c, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(hs.URL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: h})
	return c, err
}

func read(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func TestAuth(t *testing.T) {
	_, hs := setup(t)
	if _, err := dial(t, hs, "wrong"); err == nil {
		t.Fatal("wrong token accepted")
	}
	resp, err := http.Get(hs.URL + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("healthz: %v %v", resp, err)
	}
}

func TestHelloCommandsAndPushes(t *testing.T) {
	srv, hs := setup(t)
	c, err := dial(t, hs, "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	hello := read(t, c)
	if hello["type"] != "hello" || hello["api"] != float64(APIVersion) || len(hello["robots"].([]any)) != 1 {
		t.Fatalf("hello: %v", hello)
	}

	send := func(s string) {
		if err := c.Write(context.Background(), websocket.MessageText, []byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	send(`{"id":1,"command":"say_text","robot":"00a1b2c3","args":{"text":"hi"}}`)
	if r := read(t, c); r["id"] != float64(1) || r["success"] != true || r["result"].(map[string]any)["echo"] != `"hi"` {
		t.Fatalf("result: %v", r)
	}
	send(`{"id":2,"command":"fail"}`)
	if r := read(t, c); r["success"] != false || r["error"] != "nope" {
		t.Fatalf("error result: %v", r)
	}
	send(`{"id":3,"command":"x","robot":"other"}`)
	if r := read(t, c); r["error"] != "unknown robot" {
		t.Fatalf("unknown robot: %v", r)
	}

	srv.Event("00a1b2c3", robot.Event{Name: "touched"})
	if e := read(t, c); e["event"] != "touched" || e["robot"] != "00a1b2c3" {
		t.Fatalf("event: %v", e)
	}
	srv.State(robot.State{ESN: "00a1b2c3", Touched: true})
	if e := read(t, c); e["event"] != "state" || e["state"].(map[string]any)["touched"] != true {
		t.Fatalf("state: %v", e)
	}
}

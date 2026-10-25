// vector-ws: development client for the bridge WebSocket API. Prints hello/events and runs
// commands given as arguments, one after another:
//
//	go run ./cmd/vector-ws -url ws://127.0.0.1:3012/ws 'set_head_angle {"angle":30}' 'say_text {"text":"hi"}'
//	go run ./cmd/vector-ws -listen 30s     # only watch events
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"
)

func main() {
	url := flag.String("url", "ws://127.0.0.1:3012/ws", "bridge WebSocket URL")
	token := flag.String("token", os.Getenv("VECTOR_TOKEN"), "API token")
	listen := flag.Duration("listen", 3*time.Second, "keep printing events this long after the last command")
	full := flag.Bool("full", false, "print full state messages")
	flag.Parse()

	ctx := context.Background()
	h := http.Header{}
	if *token != "" {
		h.Set("Authorization", "Bearer "+*token)
	}
	c, _, err := websocket.Dial(ctx, *url, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		log.Fatal(err)
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 22)
	t0 := time.Now()
	results := make(chan map[string]any, 8)
	go func() {
		for {
			_, b, err := c.Read(ctx)
			if err != nil {
				log.Printf("read: %v", err)
				os.Exit(1)
			}
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			if m["type"] == "result" {
				results <- m
			}
			if !*full && (m["type"] == "hello" || m["event"] == "state") {
				if len(b) > 240 {
					b = append(b[:240:240], []byte("…")...)
				}
			}
			fmt.Printf("+%6.2fs %s\n", time.Since(t0).Seconds(), b)
		}
	}()
	for i, arg := range flag.Args() {
		name, rest, _ := strings.Cut(arg, " ")
		args := json.RawMessage("{}")
		if strings.TrimSpace(rest) != "" {
			args = json.RawMessage(rest)
		}
		req, _ := json.Marshal(map[string]any{"id": i + 1, "command": name, "args": args})
		fmt.Printf("+%6.2fs >>> %s\n", time.Since(t0).Seconds(), req)
		if err := c.Write(ctx, websocket.MessageText, req); err != nil {
			log.Fatal(err)
		}
		<-results
	}
	time.Sleep(*listen)
}

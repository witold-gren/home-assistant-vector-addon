// Package api serves the bridge to the Home Assistant integration: WebSocket /ws (hello, state and
// event pushes, commands) and /healthz.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/robot"
)

// APIVersion changes when the WebSocket protocol changes incompatibly.
const APIVersion = 1

// Robot is what the server needs from a robot manager.
type Robot interface {
	ESN() string
	Snapshot() robot.State
	Command(ctx context.Context, name string, args robot.Args) (any, error)
}

type Server struct {
	Version string
	Token   string
	Log     *slog.Logger

	// Router (optional) answers speech_decision / set_voice_router and is announced in hello.
	Router VoiceRouter

	mu      sync.Mutex
	robots  []Robot
	clients map[*client]struct{}
}

type client struct {
	out chan []byte
}

// VoiceRouter is the part of the voice router the API exposes.
type VoiceRouter interface {
	Decide(id int64, handled bool) bool
	SetEnabled(ctx context.Context, on bool) error
	LLMReply(id int64, text string) bool
	SetLLM(ctx context.Context, on bool) error
	Status() map[string]any
}

// Clients is the number of connected integrations (the voice router passes when there is none).
func (s *Server) Clients() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

// RouterChanged pushes the router status to the integrations.
func (s *Server) RouterChanged() {
	if s.Router != nil {
		s.broadcast(map[string]any{"type": "event", "event": "voice_router", "data": s.Router.Status()})
	}
}

func New(version, token string, log *slog.Logger) *Server {
	return &Server{Version: version, Token: token, Log: log, clients: map[*client]struct{}{}}
}

// AddRobot registers a robot for hello and commands.
func (s *Server) AddRobot(r Robot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.robots = append(s.robots, r)
}

func (s *Server) robot(esn string) Robot {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.robots {
		if esn == "" || strings.EqualFold(r.ESN(), esn) {
			return r
		}
	}
	return nil
}

// State implements robot.Sink.
func (s *Server) State(st robot.State) {
	s.broadcast(map[string]any{"type": "event", "event": "state", "robot": st.ESN, "state": st})
}

// Event implements robot.Sink.
func (s *Server) Event(esn string, ev robot.Event) {
	s.Log.Debug("event", "robot", esn, "event", ev.Name, "data", ev.Data)
	s.broadcast(map[string]any{"type": "event", "event": ev.Name, "robot": esn, "data": ev.Data})
}

func (s *Server) broadcast(msg any) {
	b, err := json.Marshal(msg)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		select {
		case c.out <- b:
		default: // a stuck client misses messages; the next state push is complete anyway
		}
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /ws", s.ws)
	for _, prefix := range []string{"", "/{esn}"} {
		mux.HandleFunc("GET /snapshot"+prefix, s.snapshot)
		mux.HandleFunc("GET /mjpeg"+prefix, s.mjpeg)
		mux.HandleFunc("POST /audio"+prefix, s.audio)
	}
	mux.HandleFunc("GET /photo/{esn}/{id}", s.photo)
	return mux
}

func (s *Server) authorized(r *http.Request) bool {
	if s.Token == "" {
		return true
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if got == "" {
		got = r.URL.Query().Get("token")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) == 1
}

type request struct {
	ID      int64      `json:"id"`
	Command string     `json:"command"`
	Robot   string     `json:"robot"`
	Args    robot.Args `json:"args"`
}

func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	conn.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	c := &client{out: make(chan []byte, 256)}
	s.mu.Lock()
	states := make([]robot.State, 0, len(s.robots))
	for _, rb := range s.robots {
		states = append(states, rb.Snapshot())
	}
	helloMsg := map[string]any{"type": "hello", "version": s.Version, "api": APIVersion, "robots": states}
	if s.Router != nil {
		helloMsg["voice_router"] = s.Router.Status()
	}
	hello, _ := json.Marshal(helloMsg)
	c.out <- hello
	s.clients[c] = struct{}{}
	s.mu.Unlock()
	s.Log.Info("integration connected", "remote", r.RemoteAddr)
	defer func() {
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
		s.Log.Info("integration disconnected", "remote", r.RemoteAddr)
	}()

	go func() {
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case b := <-c.out:
				wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Write(wctx, websocket.MessageText, b)
				wcancel()
				if err != nil {
					return
				}
			}
		}
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			_ = conn.CloseNow()
			return
		}
		var req request
		if err := json.Unmarshal(data, &req); err != nil || req.Command == "" {
			continue
		}
		// commands run concurrently: a long action must not delay `stop`
		go s.run(ctx, c, req)
	}
}

func (s *Server) run(ctx context.Context, c *client, req request) {
	reply := map[string]any{"type": "result", "id": req.ID}
	rb := s.robot(req.Robot)
	var res any
	var err error
	if s.Router != nil && (req.Command == "speech_decision" || req.Command == "set_voice_router" ||
		req.Command == "llm_reply" || req.Command == "set_llm_proxy") {
		res, err = s.routerCommand(ctx, req)
	} else if rb == nil {
		err = errUnknownRobot
	} else {
		cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
		res, err = rb.Command(cctx, req.Command, req.Args)
		cancel()
	}
	if err != nil {
		s.Log.Info("command failed", "command", req.Command, "err", err)
		reply["success"], reply["error"] = false, err.Error()
	} else {
		reply["success"], reply["result"] = true, res
	}
	b, _ := json.Marshal(reply)
	select {
	case c.out <- b:
	case <-ctx.Done():
	}
}

func (s *Server) routerCommand(ctx context.Context, req request) (any, error) {
	var args struct {
		ID      int64  `json:"id"`
		Handled bool   `json:"handled"`
		Enabled bool   `json:"enabled"`
		Text    string `json:"text"`
	}
	raw, _ := json.Marshal(req.Args)
	_ = json.Unmarshal(raw, &args)
	switch req.Command {
	case "llm_reply":
		if !s.Router.LLMReply(args.ID, args.Text) {
			return nil, apiError("LLM request expired")
		}
		return nil, nil
	case "set_llm_proxy":
		err := s.Router.SetLLM(ctx, args.Enabled)
		return s.Router.Status(), err
	}
	if req.Command == "speech_decision" {
		if !s.Router.Decide(args.ID, args.Handled) {
			return nil, apiError("speech request expired")
		}
		return nil, nil
	}
	err := s.Router.SetEnabled(ctx, args.Enabled)
	return s.Router.Status(), err
}

type apiError string

func (e apiError) Error() string { return string(e) }

const errUnknownRobot = apiError("unknown robot")

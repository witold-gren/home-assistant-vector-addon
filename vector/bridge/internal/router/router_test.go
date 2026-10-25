package router

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/robot"
	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/wirepod"
)

// fakeWirepod keeps custom intents in memory like wire-pod does.
type fakeWirepod struct {
	mu      sync.Mutex
	intents []wirepod.CustomIntent
	kg      map[string]any
}

func (f *fakeWirepod) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/api/get_custom_intents_json":
		if len(f.intents) == 0 {
			http.Error(w, "you must create an intent first", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(f.intents)
	case "/api/add_custom_intent":
		var in wirepod.CustomIntent
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.intents = append(f.intents, in)
	case "/api/get_kg_api":
		_ = json.NewEncoder(w).Encode(f.kg)
	case "/api/set_kg_api":
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		for k, v := range m {
			f.kg[k] = v
		}
	case "/api/remove_custom_intent":
		var req struct{ Number int }
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.intents = append(f.intents[:req.Number-1], f.intents[req.Number:]...)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeWirepod) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, in := range f.intents {
		out = append(out, in.Name)
	}
	return out
}

type sink struct {
	mu      sync.Mutex
	clients int
	events  []robot.Event
	onEvent func(robot.Event)
}

func (s *sink) Event(_ string, ev robot.Event) {
	s.mu.Lock()
	s.events = append(s.events, ev)
	f := s.onEvent
	s.mu.Unlock()
	if f != nil {
		f(ev)
	}
}
func (s *sink) Clients() int { s.mu.Lock(); defer s.mu.Unlock(); return s.clients }

func newRouter(t *testing.T, wp *fakeWirepod, sk *sink) *Router {
	hs := httptest.NewServer(wp)
	t.Cleanup(hs.Close)
	return New(Config{
		WP: wirepod.New(hs.URL), Token: "tok", HookURL: "http://192.168.1.10:8790/",
		StateFile: filepath.Join(t.TempDir(), "voice_router"),
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)), Sink: sk,
	}, true)
}

func TestSyncIntent(t *testing.T) {
	wp := &fakeWirepod{intents: []wirepod.CustomIntent{{Name: "user_intent"}}}
	r := newRouter(t, wp, &sink{})
	ctx := context.Background()
	if err := r.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := wp.names(); strings.Join(got, ",") != "user_intent,ha_router" {
		t.Fatalf("after install: %v", got)
	}
	in := wp.intents[1]
	if !in.IsSystem || in.Utterances[0] != "*" || in.ExecArgs[len(in.ExecArgs)-1] != "http://192.168.1.10:8790/wirepod/speech?token=tok" {
		t.Fatalf("intent %+v", in)
	}
	// idempotent
	_ = r.Sync(ctx)
	if len(wp.names()) != 2 {
		t.Fatalf("second sync changed intents: %v", wp.names())
	}
	// an outdated copy (old token) and a duplicate are replaced by one fresh intent
	wp.intents[1].ExecArgs = []string{"old"}
	wp.intents = append(wp.intents, wp.intents[1])
	_ = r.Sync(ctx)
	if got := wp.names(); strings.Join(got, ",") != "user_intent,ha_router" || wp.intents[1].ExecArgs[0] != "-s" {
		t.Fatalf("after repair: %v", wp.intents)
	}
	// off: removed, the user's intent stays; the state survives a restart
	if err := r.SetEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	if got := wp.names(); strings.Join(got, ",") != "user_intent" {
		t.Fatalf("after disable: %v", got)
	}
	if New(r.Config, true).Enabled() {
		t.Fatal("off state must persist")
	}
}

func post(t *testing.T, h http.Handler, token, body string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/wirepod/speech?token="+token, strings.NewReader(body))
	req.Header.Set("User-Agent", "00a1b2c3")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		return rec.Result().Status
	}
	return strings.TrimSpace(rec.Body.String())
}

func TestHook(t *testing.T) {
	decisionWait = 300 * time.Millisecond
	sk := &sink{}
	r := newRouter(t, &fakeWirepod{}, sk)
	h := r.Handler()

	if got := post(t, h, "bad", `"x"`); !strings.HasPrefix(got, "401") {
		t.Fatalf("bad token: %s", got)
	}
	// no integration connected: wire-pod handles it, no event
	if got := post(t, h, "tok", `"turn on the light"`); got != string(passReply) || len(sk.events) != 0 {
		t.Fatalf("no clients: %s %v", got, sk.events)
	}

	sk.clients = 1
	sk.onEvent = func(ev robot.Event) {
		go r.Decide(ev.Data["id"].(int64), strings.HasPrefix(ev.Data["text"].(string), "turn on"))
	}
	if got := post(t, h, "tok", `"turn on the living room light"`); got != string(handledReply) {
		t.Fatalf("handled: %s", got)
	}
	if sk.events[0].Name != "speech" || sk.events[0].Data["text"] != "turn on the living room light" {
		t.Fatalf("event %v (quotes must be stripped)", sk.events[0])
	}
	if got := post(t, h, "tok", `"what time is it"`); got != string(passReply) {
		t.Fatalf("not handled: %s", got)
	}

	// no decision in time
	sk.onEvent = nil
	if got := post(t, h, "tok", `"anything"`); got != string(passReply) {
		t.Fatalf("timeout: %s", got)
	}
	// router off
	r.enabled.Store(false)
	if got := post(t, h, "tok", `"turn on"`); got != string(passReply) {
		t.Fatalf("off: %s", got)
	}
}

func TestLLMProxy(t *testing.T) {
	llmWait = 500 * time.Millisecond
	wp := &fakeWirepod{kg: map[string]any{"enable": true, "provider": "custom", "endpoint": "http://llm:11434/v1",
		"key": "sk-local", "model": "llama3", "openai_prompt": "You are Vector"}}
	sk := &sink{clients: 1}
	r := newRouter(t, wp, sk)
	ctx := context.Background()
	if err := r.SetLLM(ctx, true); err != nil {
		t.Fatal(err)
	}
	if !r.LLMEnabled() || wp.kg["endpoint"] != "http://192.168.1.10:8790/v1" || wp.kg["key"] != "tok" || wp.kg["openai_prompt"] != "You are Vector" {
		t.Fatalf("kg after on: %v", wp.kg)
	}
	sk.onEvent = func(ev robot.Event) {
		if ev.Name == "llm" {
			go r.LLMReply(ev.Data["id"].(int64), "Warsaw is the capital of Poland.")
		}
	}
	body := `{"model":"home-assistant","stream":true,"messages":[{"role":"system","content":"x"},{"role":"user","content":"what is the capital of poland"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, req)
	out := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(out, `"content":"Warsaw is the capital of Poland."`) || !strings.HasSuffix(out, "data: [DONE]\n\n") {
		t.Fatalf("stream: %d %s", rec.Code, out)
	}
	if sk.events[len(sk.events)-1].Data["text"] != "what is the capital of poland" {
		t.Fatalf("event %v", sk.events)
	}
	// bad key
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec = httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("no key: %d", rec.Code)
	}
	if err := r.SetLLM(ctx, false); err != nil {
		t.Fatal(err)
	}
	if r.LLMEnabled() || wp.kg["endpoint"] != "http://llm:11434/v1" || wp.kg["model"] != "llama3" || wp.kg["key"] != "sk-local" {
		t.Fatalf("kg after off: %v", wp.kg)
	}
}

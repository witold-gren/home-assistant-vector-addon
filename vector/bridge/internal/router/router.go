// Package router hands what Vector heard to Home Assistant. wire-pod calls the hook through its
// system intent `ha_router` (utterance `*`, matched before its own intents); the integration decides
// within a time budget whether Home Assistant handled the sentence. Handled → wire-pod gets
// `intent_system_noaudio` (Vector stays silent, the integration answers); anything else, or no
// answer in time → wire-pod handles the sentence as before. Without Home Assistant nothing changes.
package router

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/robot"
	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/wirepod"
)

// IntentName of the system intent the router keeps in wire-pod.
const IntentName = "ha_router"

var (
	decisionWait = 8 * time.Second // the integration's answer; curl in wire-pod waits a bit longer
	curlTimeout  = "10"
)

// Sink receives the `speech` events for the integration.
type Sink interface {
	Event(esn string, ev robot.Event)
	Clients() int
}

// Config of the router.
type Config struct {
	WP        *wirepod.Client
	Token     string
	HookURL   string // how wire-pod reaches the hook, e.g. http://192.168.1.10:8790
	StateFile string // persists on/off
	Log       *slog.Logger
	Sink      Sink
	OnChange  func(enabled bool)
}

type Router struct {
	Config

	enabled    atomic.Bool
	llmOn      atomic.Bool // without a state file
	ids        atomic.Int64
	mu         sync.Mutex
	pending    map[int64]chan bool
	llmPending map[int64]chan string
	syncMu     sync.Mutex
}

// New loads the saved on/off state (default `enabled`).
func New(cfg Config, enabled bool) *Router {
	r := &Router{Config: cfg, pending: map[int64]chan bool{}, llmPending: map[int64]chan string{}}
	if b, err := os.ReadFile(cfg.StateFile); err == nil {
		enabled = strings.TrimSpace(string(b)) == "on"
	}
	r.enabled.Store(enabled)
	return r
}

func (r *Router) Enabled() bool { return r.enabled.Load() }

// SetEnabled switches the router and updates the wire-pod intent.
func (r *Router) SetEnabled(ctx context.Context, on bool) error {
	r.enabled.Store(on)
	if r.StateFile != "" {
		state := "off"
		if on {
			state = "on"
		}
		_ = os.WriteFile(r.StateFile, []byte(state), 0o600)
	}
	if r.OnChange != nil {
		r.OnChange(on)
	}
	return r.Sync(ctx)
}

// Intent is the wire-pod system intent pointing at this hook.
func (r *Router) Intent() wirepod.CustomIntent {
	return wirepod.CustomIntent{
		Name:        IntentName,
		Description: "Home Assistant (Vector add-on) — managed automatically, do not edit",
		Utterances:  []string{"*"},
		Intent:      "intent_system_noaudio",
		Exec:        "/usr/bin/curl",
		// wire-pod substitutes whole arguments only: !speechText (in quotes), !botSerial
		ExecArgs: []string{"-s", "-m", curlTimeout, "-A", "!botSerial", "-H", "Content-Type: text/plain",
			"--data-binary", "!speechText", strings.TrimRight(r.HookURL, "/") + "/wirepod/speech?token=" + r.Token},
		IsSystem: true,
	}
}

func same(a, b wirepod.CustomIntent) bool {
	return a.Name == b.Name && a.Intent == b.Intent && a.Exec == b.Exec && a.IsSystem == b.IsSystem &&
		slices.Equal(a.Utterances, b.Utterances) && slices.Equal(a.ExecArgs, b.ExecArgs)
}

// Sync makes wire-pod hold exactly one up-to-date ha_router intent while enabled, none otherwise.
func (r *Router) Sync(ctx context.Context) error {
	r.syncMu.Lock()
	defer r.syncMu.Unlock()
	if r.WP == nil || r.HookURL == "" {
		return fmt.Errorf("voice router needs wire-pod and a hook address")
	}
	list, err := r.WP.CustomIntents(ctx)
	if err != nil {
		return err
	}
	want := r.Intent()
	var ours []int // 1-based positions
	ok := false
	for i, in := range list {
		if in.Name != IntentName {
			continue
		}
		if r.Enabled() && !ok && same(in, want) {
			ok = true
			continue
		}
		ours = append(ours, i+1)
	}
	// remove from the back so earlier positions stay valid
	for i := len(ours) - 1; i >= 0; i-- {
		if err := r.WP.RemoveCustomIntent(ctx, ours[i]); err != nil {
			return err
		}
	}
	if r.Enabled() && !ok {
		if err := r.WP.AddCustomIntent(ctx, want); err != nil {
			return err
		}
		r.Log.Info("voice router: wire-pod intent installed", "hook", r.HookURL)
	} else if len(ours) > 0 {
		r.Log.Info("voice router: wire-pod intent removed", "count", len(ours))
	}
	return nil
}

// Run keeps the intent in place (wire-pod may restart or be edited) until ctx ends.
func (r *Router) Run(ctx context.Context) {
	for {
		if err := r.Sync(ctx); err != nil {
			r.Log.Warn("voice router: wire-pod sync", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Minute):
		}
	}
}

// Decide delivers the integration's answer for speech `id`.
func (r *Router) Decide(id int64, handled bool) bool {
	r.mu.Lock()
	ch, ok := r.pending[id]
	delete(r.pending, id)
	r.mu.Unlock()
	if ok {
		ch <- handled
	}
	return ok
}

var (
	handledReply = []byte(`{"status":"ok","returnIntent":"intent_system_noaudio"}`)
	passReply    = []byte(`{"status":"pass"}`)
)

// Text from wire-pod's !speechText argument: lower case, wrapped in quotes.
func cleanText(raw string) string {
	t := strings.TrimSpace(raw)
	if len(t) >= 2 && strings.HasPrefix(t, `"`) && strings.HasSuffix(t, `"`) {
		t = t[1 : len(t)-1]
	}
	return strings.TrimSpace(t)
}

// Hook serves POST /wirepod/speech?token=… for wire-pod's curl.
func (r *Router) Hook(w http.ResponseWriter, req *http.Request) {
	if subtle.ConstantTimeCompare([]byte(req.URL.Query().Get("token")), []byte(r.Token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(req.Body, 4096))
	text := cleanText(string(body))
	esn := req.Header.Get("User-Agent")
	w.Header().Set("Content-Type", "application/json")
	if text == "" || !r.Enabled() || r.Sink.Clients() == 0 {
		_, _ = w.Write(passReply)
		return
	}
	id := r.ids.Add(1)
	ch := make(chan bool, 1)
	r.mu.Lock()
	r.pending[id] = ch
	r.mu.Unlock()
	start := time.Now()
	r.Sink.Event(esn, robot.Event{Name: "speech", Data: map[string]any{"id": id, "text": text}})

	handled := false
	select {
	case handled = <-ch:
	case <-time.After(decisionWait):
		r.mu.Lock()
		delete(r.pending, id)
		r.mu.Unlock()
		r.Log.Warn("voice router: no decision in time, wire-pod handles it", "text", text)
	case <-req.Context().Done():
	}
	r.Log.Info("voice router", "text", text, "handled", handled, "took", time.Since(start).Round(time.Millisecond))
	if handled {
		_, _ = w.Write(handledReply)
	} else {
		_, _ = w.Write(passReply)
	}
}

// Handler: the public hook server (separate port, only this path).
func (r *Router) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /wirepod/speech", r.Hook)
	mux.HandleFunc("POST /v1/chat/completions", r.ChatCompletions)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return mux
}

// Status for the integration.
func (r *Router) Status() map[string]any {
	return map[string]any{"enabled": r.Enabled(), "hook": r.HookURL, "llm": r.LLMEnabled()}
}

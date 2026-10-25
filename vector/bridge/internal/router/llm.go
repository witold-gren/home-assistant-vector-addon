package router

// The wire-pod LLM ("knowledge graph", provider "custom") can point at this bridge: what wire-pod
// would ask its LLM goes to a Home Assistant conversation agent instead, through the integration.
// Switching on saves wire-pod's own LLM settings to /data and switching off restores them.

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/robot"
)

var llmWait = 25 * time.Second

// kgFields are the wire-pod LLM settings the proxy changes (and restores).
var kgFields = []string{"enable", "provider", "endpoint", "key", "model"}

func (r *Router) llmBackupFile() string {
	if r.StateFile == "" {
		return ""
	}
	return r.StateFile + "_llm_backup.json"
}

// LLMEnabled: wire-pod's LLM currently points at this bridge.
func (r *Router) LLMEnabled() bool {
	f := r.llmBackupFile()
	if f == "" {
		return r.llmOn.Load()
	}
	_, err := os.Stat(f)
	return err == nil
}

// SetLLM points wire-pod's LLM at this bridge (saving its settings) or restores them.
func (r *Router) SetLLM(ctx context.Context, on bool) error {
	if r.WP == nil || r.HookURL == "" {
		return errors.New("needs wire-pod and a hook address")
	}
	if on == r.LLMEnabled() {
		return nil
	}
	if on {
		current, err := r.WP.KGAPI(ctx)
		if err != nil {
			return err
		}
		backup := map[string]any{}
		for _, k := range kgFields {
			backup[k] = current[k]
		}
		b, _ := json.Marshal(backup)
		if f := r.llmBackupFile(); f != "" {
			if err := os.WriteFile(f, b, 0o600); err != nil {
				return err
			}
		}
		r.llmOn.Store(true)
		err = r.WP.SetKGAPI(ctx, map[string]any{
			"enable": true, "provider": "custom", "endpoint": strings.TrimRight(r.HookURL, "/") + "/v1",
			"key": r.Token, "model": "home-assistant",
		})
		if err != nil {
			_ = os.Remove(r.llmBackupFile())
			r.llmOn.Store(false)
			return err
		}
		r.Log.Info("wire-pod LLM now answered by Home Assistant", "previous_model", backup["model"])
	} else {
		var backup map[string]any
		if f := r.llmBackupFile(); f != "" {
			b, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(b, &backup); err != nil {
				return err
			}
		}
		if err := r.WP.SetKGAPI(ctx, backup); err != nil {
			return err
		}
		if f := r.llmBackupFile(); f != "" {
			_ = os.Remove(f)
		}
		r.llmOn.Store(false)
		r.Log.Info("wire-pod LLM settings restored", "model", backup["model"])
	}
	if r.OnChange != nil {
		r.OnChange(r.Enabled())
	}
	return nil
}

// LLMReply delivers the integration's answer for request `id`.
func (r *Router) LLMReply(id int64, text string) bool {
	r.mu.Lock()
	ch, ok := r.llmPending[id]
	delete(r.llmPending, id)
	r.mu.Unlock()
	if ok {
		ch <- text
	}
	return ok
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

// ChatCompletions serves POST /v1/chat/completions (OpenAI format, streaming or not).
func (r *Router) ChatCompletions(w http.ResponseWriter, req *http.Request) {
	auth := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(auth), []byte(r.Token)) != 1 {
		http.Error(w, `{"error":{"message":"unauthorized"}}`, http.StatusUnauthorized)
		return
	}
	var cr chatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<20)).Decode(&cr); err != nil {
		http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
		return
	}
	text := ""
	for i := len(cr.Messages) - 1; i >= 0; i-- {
		if cr.Messages[i].Role == "user" {
			text = strings.TrimSpace(cr.Messages[i].Content)
			break
		}
	}
	if text == "" || r.Sink.Clients() == 0 {
		http.Error(w, `{"error":{"message":"Home Assistant is not connected"}}`, http.StatusServiceUnavailable)
		return
	}
	id := r.ids.Add(1)
	ch := make(chan string, 1)
	r.mu.Lock()
	r.llmPending[id] = ch
	r.mu.Unlock()
	start := time.Now()
	r.Sink.Event("", robot.Event{Name: "llm", Data: map[string]any{"id": id, "text": text}})
	var answer string
	select {
	case answer = <-ch:
	case <-time.After(llmWait):
		r.mu.Lock()
		delete(r.llmPending, id)
		r.mu.Unlock()
		r.Log.Warn("LLM proxy: no answer from Home Assistant in time", "text", text)
		http.Error(w, `{"error":{"message":"timeout"}}`, http.StatusGatewayTimeout)
		return
	case <-req.Context().Done():
		return
	}
	r.Log.Info("LLM proxy", "text", text, "took", time.Since(start).Round(time.Millisecond))
	writeCompletion(w, cr, id, answer)
}

func writeCompletion(w http.ResponseWriter, cr chatRequest, id int64, answer string) {
	model := cr.Model
	if model == "" {
		model = "home-assistant"
	}
	cid := fmt.Sprintf("chatcmpl-ha-%d", id)
	created := time.Now().Unix()
	if !cr.Stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": cid, "object": "chat.completion", "created": created, "model": model,
			"choices": []any{map[string]any{"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": answer}}},
		})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	chunk := func(delta map[string]any, finish any) {
		b, _ := json.Marshal(map[string]any{
			"id": cid, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher != nil {
			flusher.Flush()
		}
	}
	chunk(map[string]any{"role": "assistant", "content": ""}, nil)
	chunk(map[string]any{"content": answer}, nil)
	chunk(map[string]any{}, "stop")
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

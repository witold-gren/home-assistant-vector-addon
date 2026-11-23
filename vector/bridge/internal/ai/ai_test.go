package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	if c, err := New("", "x"); c != nil || err != nil {
		t.Fatalf("empty url: %v %v", c, err)
	}
	if _, err := New("ftp://x", "m"); err == nil {
		t.Fatal("ftp accepted")
	}
	if _, err := New("http://192.168.1.30:11434", ""); err == nil {
		t.Fatal("no model accepted")
	}
	c, err := New("http://192.168.1.30:11434/", " m:4b ")
	if err != nil || c.URL != "http://192.168.1.30:11434" || c.Model != "m:4b" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestOff(t *testing.T) {
	var c *Client
	if _, err := c.Ask(context.Background(), "ile nóg ma pająk?", 20); err != ErrOff {
		t.Fatalf("err %v", err)
	}
}

func fakeOllama(t *testing.T, reply string, status int, seen *map[string]any) *Client {
	t.Helper()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if seen != nil {
			_ = json.NewDecoder(r.Body).Decode(seen)
		}
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = w.Write([]byte(`{"error":"model 'x' not found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"role": "assistant", "content": reply}, "done": true})
	}))
	t.Cleanup(hs.Close)
	c, err := New(hs.URL, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAsk(t *testing.T) {
	var seen map[string]any
	c := fakeOllama(t, "Pająk ma **osiem** nóg! 🕷️ Więcej na https://example.com/pajak", 200, &seen)
	a, err := c.Ask(context.Background(), "  ile nóg   ma pająk? ", 20)
	if err != nil {
		t.Fatal(err)
	}
	if a.Text != "Pająk ma osiem nóg! Więcej na." || a.Model != "test-model" {
		t.Fatalf("answer %+v", a)
	}
	if seen["stream"] != false || seen["model"] != "test-model" {
		t.Fatalf("request %v", seen)
	}
	msgs := seen["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("history sent: %v", msgs)
	}
	sys := msgs[0].(map[string]any)["content"].(string)
	if !strings.Contains(sys, "najwyżej 20 słów") || !strings.Contains(sys, "rodzica") {
		t.Fatalf("system prompt %q", sys)
	}
	if u := msgs[1].(map[string]any)["content"]; u != "ile nóg ma pająk?" {
		t.Fatalf("question %q", u)
	}
}

func TestAskErrors(t *testing.T) {
	c := fakeOllama(t, "", 404, nil)
	if _, err := c.Ask(context.Background(), "x", 20); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err %v", err)
	}
	c = fakeOllama(t, "<think>hmm</think>   ", 200, nil)
	if _, err := c.Ask(context.Background(), "x", 20); err == nil {
		t.Fatal("empty answer accepted")
	}
	if _, err := c.Ask(context.Background(), strings.Repeat("a", MaxQuestion+1), 20); err == nil {
		t.Fatal("long question accepted")
	}
	if _, err := c.Ask(context.Background(), "   ", 20); err == nil {
		t.Fatal("empty question accepted")
	}
}

func TestAskTimeout(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer hs.Close()
	c, _ := New(hs.URL, "m")
	c.Timeout = 150 * time.Millisecond
	_, err := c.Ask(context.Background(), "x", 20)
	if err == nil || !strings.Contains(err.Error(), "no answer within") {
		t.Fatalf("err %v", err)
	}
}

func TestClean(t *testing.T) {
	for in, want := range map[string]string{
		"<think>długie myślenie</think>Słońce to gwiazda.":            "Słońce to gwiazda.",
		"# Tytuł\n- punkt jeden\n- punkt dwa":                         "Tytuł - punkt jeden - punkt dwa.",
		"Napisz na kot@example.com albo zadzwoń 123 456 789, dobrze?": "Napisz na albo zadzwoń , dobrze?",
		"Zajrzyj na www.strona.pl lub wikipedia.org po więcej":        "Zajrzyj na lub po więcej.",
		"Temperatura to 20°C, a 2+2=4.":                               "Temperatura to 20°C, a 2+2=4.",
	} {
		if got := Clean(in, 20); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Clean("Raz dwa trzy. Cztery pięć sześć siedem osiem", 6); got != "Raz dwa trzy." {
		t.Errorf("sentence cut %q", got)
	}
	// no sentence end near the limit: hard cut + full stop
	if got := Clean("a b c d e f g h", 5); got != "a b c d e." {
		t.Errorf("hard cut %q", got)
	}
}

package wyoming

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// fakePiper is a Wyoming TTS server on localhost: it answers describe and synthesize, and records
// what it was asked. Data arrives as a separate block (newer clients) — both forms are read.
type fakePiper struct {
	ln       net.Listener
	asked    chan map[string]any
	inline   bool // send event data inline in the header (older servers)
	fail     bool // answer synthesize with an error event
	noFormat bool
}

func newFakePiper(t *testing.T) *fakePiper {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakePiper{ln: ln, asked: make(chan map[string]any, 4)}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakePiper) send(w io.Writer, typ string, data map[string]any, payload []byte) {
	h := map[string]any{"type": typ}
	var body []byte
	if f.inline {
		h["data"] = data
	} else if data != nil {
		body, _ = json.Marshal(data)
		h["data_length"] = len(body)
	}
	if len(payload) > 0 {
		h["payload_length"] = len(payload)
	}
	line, _ := json.Marshal(h)
	_, _ = w.Write(append(append(append(line, '\n'), body...), payload...))
}

func (f *fakePiper) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return
	}
	var h struct {
		Type       string         `json:"type"`
		Data       map[string]any `json:"data"`
		DataLength int            `json:"data_length"`
	}
	_ = json.Unmarshal(line, &h)
	if h.DataLength > 0 {
		b := make([]byte, h.DataLength)
		_, _ = io.ReadFull(r, b)
		_ = json.Unmarshal(b, &h.Data)
	}
	switch h.Type {
	case "describe":
		f.send(c, "info", map[string]any{"tts": []any{map[string]any{"name": "piper", "voices": []any{
			map[string]any{"name": "pl_PL-gosia-medium", "description": "Gosia", "languages": []any{"pl_PL"}, "installed": true},
			map[string]any{"name": "pl_PL-darkman-medium", "languages": []any{"pl_PL"}, "installed": true,
				"speakers": []any{map[string]any{"name": "a"}}},
			map[string]any{"name": "en_US-lessac-medium", "languages": []any{"en_US"}, "installed": false},
		}}}}, nil)
	case "synthesize":
		f.asked <- h.Data
		if f.fail {
			f.send(c, "error", map[string]any{"text": "voice not found"}, nil)
			return
		}
		format := map[string]any{"rate": 22050, "width": 2, "channels": 1}
		if f.noFormat {
			format = map[string]any{}
		}
		f.send(c, "audio-start", format, nil)
		f.send(c, "audio-chunk", format, make([]byte, 1000))
		f.send(c, "audio-chunk", format, make([]byte, 500))
		f.send(c, "audio-stop", nil, nil)
	}
}

func (f *fakePiper) client(t *testing.T) *Client {
	t.Helper()
	c, err := New("tcp://"+f.ln.Addr().String(), "pl_PL-gosia-medium")
	if err != nil {
		t.Fatal(err)
	}
	c.Timeout = 3 * time.Second
	return c
}

func TestNewAddress(t *testing.T) {
	for in, want := range map[string]string{
		"tcp://192.168.1.20:10200": "192.168.1.20:10200",
		"192.168.1.20:10201":       "192.168.1.20:10201",
		"piper.local":              "piper.local:10200",
		"tcp://piper.local":        "piper.local:10200",
	} {
		c, err := New(in, "")
		if err != nil || c.Addr != want {
			t.Errorf("%s → %v %v, want %s", in, c, err, want)
		}
	}
	for _, bad := range []string{"", "http://piper:10200"} {
		if _, err := New(bad, ""); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestVoices(t *testing.T) {
	for _, inline := range []bool{false, true} {
		f := newFakePiper(t)
		f.inline = inline
		v, err := f.client(t).Voices(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(v) != 2 || v[0].Name != "pl_PL-gosia-medium" || v[0].Description != "Gosia" || v[1].Speakers[0] != "a" {
			t.Fatalf("inline=%v voices %+v", inline, v)
		}
	}
}

func TestSynthesizeWAV(t *testing.T) {
	f := newFakePiper(t)
	wav, err := f.client(t).Synthesize(context.Background(), "Cześć!", "")
	if err != nil {
		t.Fatal(err)
	}
	asked := <-f.asked
	if asked["text"] != "Cześć!" || asked["voice"].(map[string]any)["name"] != "pl_PL-gosia-medium" {
		t.Fatalf("asked %v", asked)
	}
	if !strings.HasPrefix(string(wav), "RIFF") || len(wav) != 44+1500 {
		t.Fatalf("wav %d bytes", len(wav))
	}
	if rate := binary.LittleEndian.Uint32(wav[24:28]); rate != 22050 {
		t.Fatalf("rate %d", rate)
	}
	// another voice
	if _, err := f.client(t).Synthesize(context.Background(), "x", "pl_PL-darkman-medium"); err != nil {
		t.Fatal(err)
	}
	if v := (<-f.asked)["voice"].(map[string]any)["name"]; v != "pl_PL-darkman-medium" {
		t.Fatalf("voice %v", v)
	}
}

func TestSynthesizeErrors(t *testing.T) {
	f := newFakePiper(t)
	f.fail = true
	if _, err := f.client(t).Synthesize(context.Background(), "x", ""); err == nil || !strings.Contains(err.Error(), "voice not found") {
		t.Fatalf("err %v", err)
	}
	f2 := newFakePiper(t)
	f2.noFormat = true
	if _, err := f2.client(t).Synthesize(context.Background(), "x", ""); err == nil {
		t.Fatal("no format accepted")
	}
	// nobody listens
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	c, _ := New(addr, "")
	if _, err := c.Synthesize(context.Background(), "x", ""); err == nil {
		t.Fatal("closed port accepted")
	}
}

func TestSynthesizeCancelled(t *testing.T) {
	// a server that never answers: the context ends the request
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			defer c.Close()
			time.Sleep(2 * time.Second)
		}
	}()
	c, _ := New(ln.Addr().String(), "")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.Synthesize(ctx, "x", ""); err == nil {
		t.Fatal("no error")
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancel did not end the request")
	}
}

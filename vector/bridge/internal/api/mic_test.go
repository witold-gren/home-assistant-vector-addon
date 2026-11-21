package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type micRobot struct {
	fakeRobot
	chunks  chan []byte
	stopped chan struct{}
}

func (m *micRobot) MicWatch() (<-chan []byte, func()) {
	return m.chunks, func() { close(m.stopped) }
}

func TestMicStreamsPCMAndFillsGapsWithSilence(t *testing.T) {
	micGap = 50 * time.Millisecond
	t.Cleanup(func() { micGap = 200 * time.Millisecond })
	srv := New("test", "secret", slog.New(slog.NewTextHandler(io.Discard, nil)))
	rb := &micRobot{chunks: make(chan []byte, 1), stopped: make(chan struct{})}
	srv.AddRobot(rb)
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	if resp, _ := http.Get(hs.URL + "/mic"); resp.StatusCode != 401 {
		t.Fatalf("mic without token: %d", resp.StatusCode)
	}

	rb.chunks <- []byte{1, 2, 3, 4} // waiting when the listener connects
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, hs.URL+"/mic/00a1b2c3?token=secret&rate=8000", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "audio/L16;rate=8000;channels=1" {
		t.Fatalf("content type %q", ct)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(resp.Body, got); err != nil || got[0] != 1 || got[3] != 4 {
		t.Fatalf("first chunk %v %v", got, err)
	}
	// nothing from the robot: 50 ms of silence at 8 kHz = 800 zero bytes
	silence := make([]byte, 800)
	if _, err := io.ReadFull(resp.Body, silence); err != nil {
		t.Fatal(err)
	}
	for _, b := range silence {
		if b != 0 {
			t.Fatal("the gap must be silence")
		}
	}
	cancel()
	select {
	case <-rb.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("the microphone must stop when the listener leaves")
	}
}

func TestHelloAnnouncesTheStream(t *testing.T) {
	srv, hs := setup(t)
	srv.Stream = &StreamInfo{RTSPPort: 8554, Path: "vector", Username: "vector", Audio: true}
	c, err := dial(t, hs, "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	st, ok := read(t, c)["stream"].(map[string]any)
	if !ok || st["rtsp_port"] != float64(8554) || st["path"] != "vector" || st["audio"] != true {
		t.Fatalf("stream in hello: %v", st)
	}
}

func TestHelloWithoutStream(t *testing.T) {
	_, hs := setup(t)
	c, err := dial(t, hs, "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if _, ok := read(t, c)["stream"]; ok {
		t.Fatal("no stream configured: hello must not announce one")
	}
}

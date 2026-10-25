package api

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/robot"
)

type mediaRobot struct {
	fakeRobot
	cached []byte
	frames chan []byte
}

func (m *mediaRobot) CameraLast() ([]byte, time.Time) { return m.cached, time.Unix(1792780000, 0) }
func (m *mediaRobot) CameraWatch() (<-chan []byte, func()) {
	return m.frames, func() {}
}
func (m *mediaRobot) CameraFresh(context.Context, time.Duration) ([]byte, error) {
	return []byte("fresh"), nil
}
func (m *mediaRobot) Photo(_ context.Context, id uint32, thumb bool) ([]byte, error) {
	if id != 2 {
		return nil, robot.ErrNoPhoto
	}
	if thumb {
		return []byte("thumb"), nil
	}
	return []byte("photo"), nil
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestMediaEndpoints(t *testing.T) {
	srv := New("test", "secret", slog.New(slog.NewTextHandler(io.Discard, nil)))
	rb := &mediaRobot{frames: make(chan []byte, 1)}
	srv.AddRobot(rb)
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	if code, _ := get(t, hs.URL+"/snapshot?cached=true"); code != 404 {
		t.Fatalf("no cached frame: %d", code)
	}
	rb.cached = []byte("cached")
	if code, body := get(t, hs.URL+"/snapshot/00a1b2c3?cached=true"); code != 200 || body != "cached" {
		t.Fatalf("cached: %d %q", code, body)
	}
	if code, body := get(t, hs.URL+"/snapshot"); code != 200 || body != "fresh" {
		t.Fatalf("fresh: %d %q", code, body)
	}
	if code, body := get(t, hs.URL+"/photo/00a1b2c3/2?thumb=true"); code != 200 || body != "thumb" {
		t.Fatalf("thumb: %d %q", code, body)
	}
	if code, _ := get(t, hs.URL+"/photo/00a1b2c3/7"); code != 404 {
		t.Fatalf("missing photo: %d", code)
	}
	if resp, _ := http.Get(hs.URL + "/snapshot?cached=true"); resp.StatusCode != 401 {
		t.Fatalf("snapshot without token: %d", resp.StatusCode)
	}

	// MJPEG: cached frame first, then live ones
	req, _ := http.NewRequest(http.MethodGet, hs.URL+"/mjpeg?token=secret", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "multipart/x-mixed-replace") {
		t.Fatalf("content type %q", resp.Header.Get("Content-Type"))
	}
	rb.frames <- []byte("live")
	r := bufio.NewReader(resp.Body)
	var parts []string
	for len(parts) < 2 {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			body, _ := r.ReadString('\n')
			parts = append(parts, strings.TrimSpace(body))
		}
	}
	if parts[0] != "cached" || parts[1] != "live" {
		t.Fatalf("parts %v", parts)
	}
}

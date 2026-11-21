package api

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeViewer is an internal "ffmpeg" the test drives.
type fakeViewer struct {
	lines   chan string
	done    chan struct{}
	mu      sync.Mutex
	stopped bool
}

func (f *fakeViewer) viewer() *viewer {
	return &viewer{lines: f.lines, done: f.done, stop: func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.stopped {
			f.stopped = true
			close(f.done)
		}
	}}
}

func (f *fakeViewer) isStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

func testWarmer() (*Warmer, chan *fakeViewer, *[]string) {
	started := make(chan *fakeViewer, 4)
	urls := &[]string{}
	w := &Warmer{URL: "rtsp://vector:tok@127.0.0.1:8554/vector", Timeout: 2 * time.Second, Attempts: 2,
		Hold: 50 * time.Millisecond, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	w.start = func(url string) (*viewer, error) {
		*urls = append(*urls, url)
		f := &fakeViewer{lines: make(chan string, 8), done: make(chan struct{})}
		started <- f
		return f.viewer(), nil
	}
	return w, started, urls
}

func TestWarmReturnsOnceTheStreamIsDescribedAndHoldsIt(t *testing.T) {
	w, started, urls := testWarmer()
	res := make(chan map[string]any, 1)
	go func() { r, _ := w.Warm(context.Background()); res <- r }()
	f := <-started
	f.lines <- "Input #0, rtsp, from 'rtsp://...':"
	select {
	case <-res:
		t.Fatal("ready before the stream is described")
	case <-time.After(30 * time.Millisecond):
	}
	f.lines <- "  Stream #0:0: Video: h264, 800x600"
	if r := <-res; r["ready"] != true || r["warm"] != false {
		t.Fatalf("result %v", r)
	}
	if (*urls)[0] != "rtsp://vector:tok@127.0.0.1:8554/vector" {
		t.Fatalf("url %v", *urls)
	}
	// a second call reuses the running viewer
	if r, _ := w.Warm(context.Background()); r["warm"] != true {
		t.Fatalf("second warm %v", r)
	}
	if f.isStopped() {
		t.Fatal("stopped while held")
	}
	time.Sleep(120 * time.Millisecond)
	if !f.isStopped() {
		t.Fatal("the hold must end")
	}
}

func TestWarmRetriesAMissedStart(t *testing.T) {
	w, started, _ := testWarmer()
	w.Timeout = 20 * time.Second
	res := make(chan error, 1)
	go func() { _, err := w.Warm(context.Background()); res <- err }()
	first := <-started
	close(first.lines) // go2rtc closed the stream
	second := <-started
	second.lines <- "  Stream #0:0: Video: h264"
	if err := <-res; err != nil {
		t.Fatal(err)
	}
	w.Stop()
	if !second.isStopped() {
		t.Fatal("Stop must end the internal viewer")
	}
}

func TestWarmGivesUpAfterTheTimeout(t *testing.T) {
	w, started, _ := testWarmer()
	w.Timeout = 100 * time.Millisecond
	res := make(chan error, 1)
	go func() { _, err := w.Warm(context.Background()); res <- err }()
	f := <-started
	err := <-res
	if err == nil || !strings.Contains(err.Error(), "did not start") {
		t.Fatalf("err %v", err)
	}
	if !f.isStopped() {
		t.Fatal("a failed viewer must be stopped")
	}
}

func TestWarmStreamCommandOverWebSocket(t *testing.T) {
	srv, hs := setup(t)
	w, started, _ := testWarmer()
	srv.Warm = w
	c, err := dial(t, hs, "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	read(t, c) // hello
	go func() { f := <-started; f.lines <- "  Stream #0:0: Video: h264" }()
	if err := c.Write(context.Background(), 1, []byte(`{"id":5,"command":"warm_stream","args":{}}`)); err != nil {
		t.Fatal(err)
	}
	r := read(t, c)
	if r["id"] != float64(5) || r["success"] != true {
		t.Fatalf("reply %v", r)
	}
}

func TestWarmStreamWithoutLiveStreamIsAnError(t *testing.T) {
	_, hs := setup(t)
	c, err := dial(t, hs, "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	read(t, c)
	_ = c.Write(context.Background(), 1, []byte(`{"id":6,"command":"warm_stream","args":{}}`))
	if r := read(t, c); r["success"] != false {
		t.Fatalf("reply %v", r)
	}
}

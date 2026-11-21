package api

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Warmer starts the add-on's live stream before Home Assistant's go2rtc opens it.
//
// go2rtc answers an RTSP DESCRIBE only once the stream source runs, and Home Assistant's go2rtc
// waits a hard-coded 5 s for that answer. Vector's camera needs longer when it sits on the
// charger (the bridge first probes without behavior control, then takes it): the live view
// failed, Home Assistant fell back to HLS, which failed the same way. The integration calls
// warm_stream first: an internal ffmpeg viewer starts the stream and holds it for Hold, enough
// for Home Assistant to attach to a running stream.
type Warmer struct {
	URL      string        // rtsp://vector:<token>@127.0.0.1:<port>/vector
	Timeout  time.Duration // all attempts together
	Attempts int
	Hold     time.Duration
	Log      *slog.Logger

	start func(url string) (*viewer, error) // test hook; ffmpeg by default

	mu    sync.Mutex
	cur   *viewer
	timer *time.Timer
}

// viewer is one internal ffmpeg RTSP client: its log lines, a stop function and its end.
type viewer struct {
	lines <-chan string
	done  <-chan struct{}
	stop  func()
}

// NewWarmer returns a warmer for the given stream URL with the default timings.
func NewWarmer(url string, log *slog.Logger) *Warmer {
	return &Warmer{URL: url, Timeout: 45 * time.Second, Attempts: 2, Hold: 20 * time.Second, Log: log, start: startFFmpegViewer}
}

var errStreamClosed = errors.New("go2rtc closed the stream")

// Warm returns once go2rtc describes the stream (the robot sends pictures); a second call while
// the stream is warm returns at once and extends the hold.
func (w *Warmer) Warm(ctx context.Context) (map[string]any, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cur != nil {
		select {
		case <-w.cur.done:
			w.cur = nil
		default:
			w.holdLocked(w.cur)
			return map[string]any{"ready": true, "warm": true}, nil
		}
	}
	started := time.Now()
	deadline := started.Add(w.Timeout)
	var lastErr error
	for attempt := 1; attempt <= w.Attempts; attempt++ {
		v, err := w.start(w.URL)
		if err != nil {
			return nil, err
		}
		err = waitDescribed(ctx, v, time.Until(deadline))
		if err == nil {
			w.cur = v
			w.holdLocked(v)
			w.Log.Info("live stream warm (held for Home Assistant)", "took", time.Since(started).Round(100*time.Millisecond))
			return map[string]any{"ready": true, "warm": false}, nil
		}
		v.stop()
		lastErr = err
		if time.Until(deadline) < 8*time.Second {
			break
		}
		w.Log.Info("live stream warm-up failed, retrying", "attempt", attempt, "err", err)
	}
	return nil, errors.New("the live stream did not start: " + lastErr.Error())
}

func waitDescribed(ctx context.Context, v *viewer, wait time.Duration) error {
	if wait <= 0 {
		return context.DeadlineExceeded
	}
	timeout := time.NewTimer(wait)
	defer timeout.Stop()
	for {
		select {
		case line, ok := <-v.lines:
			if !ok {
				return errStreamClosed
			}
			if strings.Contains(line, "Stream #0:") {
				return nil
			}
		case <-v.done:
			return errStreamClosed
		case <-timeout.C:
			return context.DeadlineExceeded
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (w *Warmer) holdLocked(v *viewer) {
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(w.Hold, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.cur == v {
			w.cur = nil
			v.stop()
		}
	})
}

// Stop ends the internal viewer (add-on stopping).
func (w *Warmer) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
	}
	if w.cur != nil {
		w.cur.stop()
		w.cur = nil
	}
}

func startFFmpegViewer(url string) (*viewer, error) {
	cmd := exec.Command("ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "info",
		"-analyzeduration", "0", "-probesize", "32768", "-rtsp_transport", "tcp",
		"-i", url, "-map", "0:v:0", "-c", "copy", "-f", "null", "-")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	lines := make(chan string, 64)
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			default: // nobody reads any more: drop
			}
		}
		close(lines)
		_ = cmd.Wait()
		close(done)
	}()
	var once sync.Once
	return &viewer{lines: lines, done: done, stop: func() {
		once.Do(func() {
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			}
		})
	}}, nil
}

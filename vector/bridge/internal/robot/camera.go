package robot

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

// Camera runs CameraFeed only while somebody watches (MJPEG viewers, fresh snapshots) and keeps
// the last frame for cached snapshots. On the charger Vector sends no frames without behavior
// control (FINDINGS.md): when the first frame does not come within noFrameWait, the feed restarts under
// the control lease, which then stays taken until the last viewer leaves.
type Camera struct {
	m *Manager

	mu      sync.Mutex
	viewers int
	cancel  context.CancelFunc // running feed
	idle    *time.Timer
	frame   []byte
	frameAt time.Time
	subs    map[chan []byte]struct{}
	dropped atomic.Int64 // cut frames not passed on
}

var (
	cameraLinger = 10 * time.Second // keep the feed this long after the last viewer
	noFrameWait  = 3 * time.Second  // first frame without behavior control
	stuckWait    = 10 * time.Second // no frame even under control: retry, then give up
	maxStuck     = 2
)

// ErrNoFrame: the camera gave no picture in time.
var ErrNoFrame = errors.New("no camera frame")

// CompleteJPEG says whether a frame is a whole JPEG: it starts with SOI (FF D8) and ends with EOI
// (FF D9), maybe followed by padding zeros. A cut frame lacks the end.
func CompleteJPEG(b []byte) bool {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return false
	}
	end := len(b)
	for end > 2 && b[end-1] == 0x00 {
		end--
	}
	return b[end-2] == 0xFF && b[end-1] == 0xD9
}

// Dropped counts the cut frames the camera did not pass on (tests, logs).
func (c *Camera) Dropped() int64 { return c.dropped.Load() }

func newCamera(m *Manager) *Camera { return &Camera{m: m, subs: map[chan []byte]struct{}{}} }

// Last returns the cached frame and when it was taken (nil when none yet).
func (c *Camera) Last() ([]byte, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.frame, c.frameAt
}

// Watch starts (or joins) the feed. Frames arrive on the channel (the newest wins when the reader
// is slow); call stop when done.
func (c *Camera) Watch() (<-chan []byte, func()) {
	ch := make(chan []byte, 1)
	c.mu.Lock()
	c.subs[ch] = struct{}{}
	c.viewers++
	if c.idle != nil {
		c.idle.Stop()
		c.idle = nil
	}
	if c.cancel == nil {
		ctx, cancel := context.WithCancel(context.Background())
		c.cancel = cancel
		go c.run(ctx)
	}
	c.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			delete(c.subs, ch)
			c.viewers--
			if c.viewers == 0 && c.cancel != nil {
				cancel := c.cancel
				c.idle = time.AfterFunc(cameraLinger, func() {
					c.mu.Lock()
					defer c.mu.Unlock()
					if c.viewers == 0 && c.cancel != nil {
						cancel()
						c.cancel = nil
					}
				})
			}
		})
	}
}

// Fresh returns a frame newer than maxAge, starting the feed for it when needed.
func (c *Camera) Fresh(ctx context.Context, maxAge time.Duration) ([]byte, error) {
	if f, at := c.Last(); f != nil && time.Since(at) <= maxAge {
		return f, nil
	}
	ch, stop := c.Watch()
	defer stop()
	wait, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	select {
	case f := <-ch:
		return f, nil
	case <-wait.Done():
		return nil, ErrNoFrame
	}
}

// Stop ends the feed at once (robot disconnected).
func (c *Camera) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.idle != nil {
		c.idle.Stop()
		c.idle = nil
	}
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
}

func (c *Camera) setActive(active bool) {
	c.m.update(func(t *Tracker) bool {
		if t.S.Camera == active {
			return false
		}
		t.S.Camera = active
		return true
	})
}

func (c *Camera) publish(frame []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frame, c.frameAt = frame, time.Now()
	for ch := range c.subs {
		select {
		case <-ch: // drop the stale frame the reader has not taken
		default:
		}
		ch <- frame
	}
}

// run keeps the feed going until ctx ends: retries after errors, falls back to behavior control
// and gives it up again when Vector takes it back (then probes without it first).
func (c *Camera) run(ctx context.Context) {
	c.setActive(true)
	defer c.setActive(false)
	var done func()
	var lost <-chan struct{}
	stuck := 0
	defer func() {
		if done != nil {
			done()
		}
	}()
	pause := func() {
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	for ctx.Err() == nil {
		cli, err := c.m.client()
		if err != nil {
			pause()
			continue
		}
		fctx, fcancel := context.WithCancel(ctx)
		if lost != nil {
			go func(lost <-chan struct{}) {
				select {
				case <-lost:
					fcancel()
				case <-fctx.Done():
				}
			}(lost)
		}
		probe := noFrameWait
		if done != nil {
			probe = stuckWait
		}
		got, err := c.feed(fctx, cli, probe)
		fcancel()
		if ctx.Err() != nil {
			return
		}
		if lost != nil {
			select {
			case <-lost:
				done()
				done, lost = nil, nil
				continue
			default:
			}
		}
		if !got && done != nil {
			// under control and still nothing: the robot's camera is stuck (seen after it slept on
			// the charger; wire-pod's own camera view got nothing either). Give control back.
			stuck++
			if stuck >= maxStuck {
				c.m.log.Warn("camera: no frames even with behavior control — the robot's camera seems stuck (restarting the robot helps)")
				done()
				done, lost = nil, nil
				c.setActive(false)
				<-ctx.Done()
				return
			}
			continue
		}
		stuck = 0
		if !got && done == nil && c.m.behaviorRunning() {
			// a program's Vector behavior runs without our control: taking it would stop the
			// behavior, so the view stays dark (on the charger) until it ends
			pause()
			continue
		}
		if !got && done == nil {
			c.m.log.Info("camera: no frames without behavior control, taking control while watched")
			if done, lost, err = c.m.lease.Acquire(ctx); err != nil {
				c.m.log.Warn("camera: behavior control", "err", err)
				done, lost = nil, nil
				pause()
			}
			continue
		}
		if err != nil {
			c.m.log.Debug("camera feed ended", "err", err)
		}
		pause()
	}
}

// feed streams frames until the stream ends or no frame came for `wait`. Returns whether any came.
func (c *Camera) feed(ctx context.Context, cli vectorpb.ExternalInterfaceClient, wait time.Duration) (bool, error) {
	fctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st, err := cli.CameraFeed(fctx, &vectorpb.CameraFeedRequest{})
	if err != nil {
		return false, err
	}
	got := false
	// watchdog: restarted by every frame
	watchdog := time.AfterFunc(wait, cancel)
	defer watchdog.Stop()
	for {
		r, err := st.Recv()
		if err != nil {
			return got, err
		}
		if len(r.GetData()) == 0 {
			continue
		}
		got = true
		watchdog.Reset(stuckWait)
		if data := r.GetData(); len(data) > 1 && data[0] == 0xFF && data[1] == 0xD8 && !CompleteJPEG(data) {
			// a cut frame (Wi-Fi) shows as a grey lower half in every viewer: drop it, the next one comes
			c.dropped.Add(1)
			continue
		}
		c.publish(r.GetData())
	}
}

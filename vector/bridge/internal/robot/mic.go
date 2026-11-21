package robot

import (
	"context"
	"sync"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

// Mic runs the robot's AudioFeed (mono 16-bit PCM from the microphones) while somebody
// listens, and fans the chunks out to every listener. Like the camera it only runs on demand.
type Mic struct {
	m *Manager

	mu     sync.Mutex
	cancel context.CancelFunc
	subs   map[chan []byte]struct{}
}

// micQueue: chunks buffered per listener before the oldest are dropped (a slow reader must not
// stall the feed or the other listeners).
const micQueue = 64

var micRetry = time.Second

func newMic(m *Manager) *Mic { return &Mic{m: m, subs: map[chan []byte]struct{}{}} }

// Watch starts (or joins) the feed; call stop when done. The feed ends with the last listener.
func (c *Mic) Watch() (<-chan []byte, func()) {
	ch := make(chan []byte, micQueue)
	c.mu.Lock()
	c.subs[ch] = struct{}{}
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
			if len(c.subs) == 0 && c.cancel != nil {
				c.cancel()
				c.cancel = nil
			}
		})
	}
}

// Listeners is the number of open microphone streams.
func (c *Mic) Listeners() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.subs)
}

// publish hands a chunk to every listener; a feed that was already cancelled (its stop runs under
// the same lock) must not leak a late chunk into a newer feed's listeners.
func (c *Mic) publish(ctx context.Context, chunk []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	for ch := range c.subs {
		select {
		case ch <- chunk:
		default: // reader too slow: drop its oldest chunk, keep the newest
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- chunk:
			default:
			}
		}
	}
}

// run keeps AudioFeed going until ctx ends (reconnects after errors). The first seconds are
// measured so the log shows the real sample rate of the feed.
func (c *Mic) run(ctx context.Context) {
	for ctx.Err() == nil {
		cli, err := c.m.client()
		if err == nil {
			err = c.feed(ctx, cli)
		}
		if ctx.Err() != nil {
			return
		}
		c.m.log.Debug("microphone feed ended", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(micRetry):
		}
	}
}

func (c *Mic) feed(ctx context.Context, cli vectorpb.ExternalInterfaceClient) error {
	st, err := cli.AudioFeed(ctx, &vectorpb.AudioFeedRequest{})
	if err != nil {
		return err
	}
	var first time.Time
	total, reported := 0, false
	for {
		r, err := st.Recv()
		if err != nil {
			return err
		}
		data := r.GetSignalPower()
		if len(data) == 0 {
			continue
		}
		if first.IsZero() {
			first = time.Now()
		}
		total += len(data)
		if el := time.Since(first); !reported && el >= 3*time.Second {
			reported = true
			c.m.log.Info("microphone feed", "bytes_per_s", int(float64(total)/el.Seconds()),
				"sample_rate_hz", int(float64(total)/el.Seconds()/2))
		}
		c.publish(ctx, data)
	}
}

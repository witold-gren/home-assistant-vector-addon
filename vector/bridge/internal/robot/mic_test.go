package robot

import (
	"testing"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

// AudioFeed of the fake robot: a 320-byte chunk (10 ms at 16 kHz) every 10 ms, counting feeds.
func (f *fakeRobot) AudioFeed(_ *vectorpb.AudioFeedRequest, s vectorpb.ExternalInterface_AudioFeedServer) error {
	f.mu.Lock()
	f.micFeeds++
	f.mu.Unlock()
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	for n := 0; ; n++ {
		select {
		case <-s.Context().Done():
			return nil
		case <-t.C:
			chunk := make([]byte, 320)
			chunk[0] = byte(n)
			if err := s.Send(&vectorpb.AudioFeedResponse{GroupId: uint32(n), SignalPower: chunk}); err != nil {
				return err
			}
		}
	}
}

func TestMicRunsOnlyWhileSomebodyListens(t *testing.T) {
	robot, _, m := start(t)
	a, stopA := m.Mic.Watch()
	b, stopB := m.Mic.Watch() // a second listener joins the same feed
	for _, ch := range []<-chan []byte{a, b} {
		select {
		case c := <-ch:
			if len(c) != 320 {
				t.Fatalf("chunk size %d", len(c))
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no microphone chunk")
		}
	}
	robot.get(func() {
		if robot.micFeeds != 1 {
			t.Fatalf("one feed for both listeners, got %d", robot.micFeeds)
		}
	})
	stopA()
	stopA() // stop is idempotent
	if m.Mic.Listeners() != 1 {
		t.Fatalf("listeners after one stop: %d", m.Mic.Listeners())
	}
	stopB()
	if m.Mic.Listeners() != 0 {
		t.Fatal("the feed must end with the last listener")
	}
	// a new listener starts a new feed
	c, stop := m.Mic.Watch()
	defer stop()
	select {
	case <-c:
	case <-time.After(2 * time.Second):
		t.Fatal("no chunk after restart")
	}
	robot.get(func() {
		if robot.micFeeds != 2 {
			t.Fatalf("feeds after restart: %d", robot.micFeeds)
		}
	})
}

func TestMicSlowListenerDoesNotBlockTheFeed(t *testing.T) {
	_, _, m := start(t)
	slow, stopSlow := m.Mic.Watch() // never read
	defer stopSlow()
	fast, stopFast := m.Mic.Watch()
	defer stopFast()
	deadline := time.After(3 * time.Second)
	for got := 0; got < 2*micQueue; got++ {
		select {
		case <-fast:
		case <-deadline:
			t.Fatalf("fast listener stalled after %d chunks", got)
		}
	}
	if len(slow) != micQueue {
		t.Fatalf("slow listener keeps the newest %d chunks, has %d", micQueue, len(slow))
	}
}

package robot

import (
	"testing"
	"time"
)

// 16 kHz mono s16le: 32000 bytes = 1 s
func TestAudioQueueAndVolume(t *testing.T) {
	audioLead = 50 * time.Millisecond
	robot, sink, m := start(t)
	if _, err := m.PlayAudio([]byte("6400"), -1, false); err != nil { // 0.2 s at the default volume
		t.Fatal(err)
	}
	if n, _ := m.PlayAudio([]byte("3200"), 30, false); n < 1 {
		t.Fatal("second clip must queue")
	}
	eventually(t, "playing", func() bool { return m.Snapshot().Audio.Playing })
	eventually(t, "both clips", func() (ok bool) {
		robot.get(func() { ok = len(robot.audio) == 2 && robot.audio[1].completed })
		return ok
	})
	robot.get(func() {
		a := robot.audio
		if a[0].bytes != 6400 || a[0].volume != 80 || !a[0].completed || a[1].volume != 30 {
			t.Fatalf("streams %+v", a)
		}
	})
	eventually(t, "idle", func() bool { return !m.Snapshot().Audio.Playing })
	if !sink.has("audio_started") || !sink.has("audio_finished") {
		t.Fatal("audio events")
	}
}

func TestAudioInterruptAndStop(t *testing.T) {
	audioLead = 50 * time.Millisecond
	robot, _, m := start(t)
	_, _ = m.PlayAudio([]byte("320000"), -1, false) // 10 s
	_, _ = m.PlayAudio([]byte("320000"), -1, false)
	eventually(t, "first stream", func() (ok bool) {
		robot.get(func() { ok = len(robot.audio) == 1 && robot.audio[0].bytes > 0 })
		return ok
	})
	// an announcement cuts the clip and drops the queue
	_, _ = m.PlayAudio([]byte("3200"), -1, true)
	eventually(t, "announcement played", func() (ok bool) {
		robot.get(func() { ok = len(robot.audio) == 2 && robot.audio[1].completed })
		return ok
	})
	robot.get(func() {
		if !robot.audio[0].cancelled || robot.audio[0].bytes >= 320000 {
			t.Fatalf("first clip not cut: %+v", robot.audio[0])
		}
	})
	time.Sleep(200 * time.Millisecond)
	robot.get(func() {
		if len(robot.audio) != 2 {
			t.Fatalf("queued clip must be dropped, got %d streams", len(robot.audio))
		}
	})

	_, _ = m.PlayAudio([]byte("320000"), -1, false)
	eventually(t, "third stream", func() (ok bool) {
		robot.get(func() { ok = len(robot.audio) == 3 && robot.audio[2].bytes > 0 })
		return ok
	})
	if _, err := m.Command(t.Context(), "stop_audio", nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "stopped", func() (ok bool) {
		robot.get(func() { ok = robot.audio[2].cancelled })
		return ok && !m.Snapshot().Audio.Playing
	})
}

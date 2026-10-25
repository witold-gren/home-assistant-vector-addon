package robot

import (
	"context"
	"testing"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

func TestCameraWithoutControl(t *testing.T) {
	cameraLinger, noFrameWait = 100*time.Millisecond, 200*time.Millisecond
	robot, _, m := start(t)
	frames, stop := m.Camera.Watch()
	select {
	case f := <-frames:
		if len(f) == 0 {
			t.Fatal("empty frame")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no frame")
	}
	eventually(t, "camera state", func() bool { return m.Snapshot().Camera })
	robot.get(func() {
		if robot.controls != 0 {
			t.Fatalf("control taken although frames came: %d", robot.controls)
		}
	})
	stop()
	eventually(t, "feed stops after linger", func() bool { return !m.Snapshot().Camera })
	if img, at := m.Camera.Last(); img == nil || time.Since(at) > time.Second {
		t.Fatal("last frame must stay cached")
	}
}

func TestCameraFallsBackToControl(t *testing.T) {
	cameraLinger, noFrameWait = 100*time.Millisecond, 200*time.Millisecond
	robot, _, m := start(t)
	robot.get(func() { robot.needsControl = true })
	img, err := m.Camera.Fresh(context.Background(), 0)
	if err != nil || len(img) == 0 {
		t.Fatalf("fresh frame: %v", err)
	}
	robot.get(func() {
		if robot.controls != 1 || robot.feeds < 2 {
			t.Fatalf("controls=%d feeds=%d (probe then feed under control)", robot.controls, robot.feeds)
		}
	})
	// the lease is given back once nobody watches
	eventually(t, "control released", func() (ok bool) {
		robot.get(func() { ok = robot.releases == 1 })
		return ok
	})
}

func TestPhotosAndPhotoTaken(t *testing.T) {
	robot, sink, m := start(t)
	robot.get(func() { robot.photos = []*vectorpb.PhotoInfo{{PhotoId: 2, TimestampUtc: 1687187545}} })
	cli, _ := m.client()
	// as if the photo already existed when the bridge connected (first read = baseline)
	m.mu.Lock()
	m.photosRead = false
	m.mu.Unlock()
	m.refreshPhotos(context.Background(), cli)
	if s := m.Snapshot(); s.Photos != 1 || s.LastPhoto == nil || s.LastPhoto.ID != 2 {
		t.Fatalf("photos %+v", s)
	}
	if sink.has("photo_taken") {
		t.Fatal("existing photos are not new")
	}
	robot.get(func() { robot.photos = append(robot.photos, &vectorpb.PhotoInfo{PhotoId: 3, TimestampUtc: 1792780000}) })
	m.refreshPhotos(context.Background(), cli)
	if !sink.has("photo_taken") || m.Snapshot().LastPhoto.ID != 3 {
		t.Fatal("new photo must be announced")
	}
	if img, err := m.Photo(context.Background(), 3, false); err != nil || string(img) != "jpeg" {
		t.Fatalf("photo: %q %v", img, err)
	}
	if _, err := m.Photo(context.Background(), 9, false); err != ErrNoPhoto {
		t.Fatalf("missing photo: %v", err)
	}
}

func TestCameraStuckGivesControlBack(t *testing.T) {
	cameraLinger, noFrameWait, stuckWait = 100*time.Millisecond, 100*time.Millisecond, 150*time.Millisecond
	robot, _, m := start(t)
	// frames never come, not even under control
	robot.get(func() { robot.needsControl = true; robot.holding = false })
	robot.get(func() { robot.blockFrames = true })
	_, stop := m.Camera.Watch()
	defer stop()
	eventually(t, "control given back", func() (ok bool) {
		robot.get(func() { ok = robot.controls == 1 && robot.releases == 1 })
		return ok
	})
	if m.Snapshot().Camera {
		t.Fatal("camera must be reported off after giving up")
	}
	time.Sleep(300 * time.Millisecond)
	robot.get(func() {
		if robot.controls != 1 {
			t.Fatalf("must not keep retrying: %d control requests", robot.controls)
		}
	})
}

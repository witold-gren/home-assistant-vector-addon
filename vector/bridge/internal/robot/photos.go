package robot

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

// listPhotos returns the robot's photos, newest first.
func listPhotos(ctx context.Context, cli vectorpb.ExternalInterfaceClient) ([]Photo, error) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r, err := cli.PhotosInfo(cctx, &vectorpb.PhotosInfoRequest{})
	if err != nil {
		return nil, err
	}
	photos := make([]Photo, 0, len(r.GetPhotoInfos()))
	for _, p := range r.GetPhotoInfos() {
		photos = append(photos, Photo{ID: p.GetPhotoId(), At: int64(p.GetTimestampUtc())})
	}
	sort.Slice(photos, func(i, j int) bool { return photos[i].At > photos[j].At })
	return photos, nil
}

// refreshPhotos updates the photo count and the newest photo; a newer one than before is announced
// as photo_taken (not on the first read after connecting).
func (m *Manager) refreshPhotos(ctx context.Context, cli vectorpb.ExternalInterfaceClient) {
	photos, err := listPhotos(ctx, cli)
	if err != nil {
		m.log.Debug("photos poll", "err", err)
		return
	}
	var newest *Photo
	if len(photos) > 0 {
		newest = &photos[0]
	}
	var taken *Photo
	m.mu.Lock()
	prev := m.tr.S.LastPhoto
	if m.photosRead && newest != nil && (prev == nil || newest.At > prev.At) {
		taken = newest
	}
	if len(photos) != m.tr.S.Photos || !samePhoto(prev, newest) {
		m.tr.S.Photos, m.tr.S.LastPhoto = len(photos), newest
		m.dirty = true
	}
	m.photosRead = true
	esn := m.tr.S.ESN
	m.mu.Unlock()
	if taken != nil {
		m.sink.Event(esn, Event{Name: "photo_taken", Data: map[string]any{"photo_id": taken.ID, "at": taken.At}})
	}
}

func samePhoto(a, b *Photo) bool { return a == b || a != nil && b != nil && *a == *b }

// Photo returns a stored photo (or its thumbnail) as JPEG.
func (m *Manager) Photo(ctx context.Context, id uint32, thumb bool) ([]byte, error) {
	cli, err := m.client()
	if err != nil {
		return nil, err
	}
	var img []byte
	var ok bool
	if thumb {
		r, err := cli.Thumbnail(ctx, &vectorpb.ThumbnailRequest{PhotoId: id})
		if err != nil {
			return nil, err
		}
		img, ok = r.GetImage(), r.GetSuccess()
	} else {
		r, err := cli.Photo(ctx, &vectorpb.PhotoRequest{PhotoId: id})
		if err != nil {
			return nil, err
		}
		img, ok = r.GetImage(), r.GetSuccess()
	}
	if !ok || len(img) == 0 {
		return nil, ErrNoPhoto
	}
	return img, nil
}

// ErrNoPhoto: the robot has no photo with that id.
var ErrNoPhoto = errors.New("no such photo")

// pollPhotosSoon re-reads the photo list while Vector is taking a picture (it takes a few seconds).
func (m *Manager) pollPhotosSoon(cli vectorpb.ExternalInterfaceClient) {
	go func() {
		for _, d := range []time.Duration{6 * time.Second, 8 * time.Second, 10 * time.Second} {
			time.Sleep(d)
			m.refreshPhotos(context.Background(), cli)
		}
	}()
}

// CameraLast, CameraWatch and CameraFresh expose the camera to the API server.
func (m *Manager) CameraLast() ([]byte, time.Time)      { return m.Camera.Last() }
func (m *Manager) CameraWatch() (<-chan []byte, func()) { return m.Camera.Watch() }
func (m *Manager) CameraFresh(ctx context.Context, maxAge time.Duration) ([]byte, error) {
	return m.Camera.Fresh(ctx, maxAge)
}

// PlayAudio queues an audio file on the robot speaker (see Player.Play).
func (m *Manager) PlayAudio(data []byte, volume int, interrupt bool) (int, error) {
	return m.Player.Play(data, volume, interrupt)
}

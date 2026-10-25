package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/robot"
)

// Media is what the image endpoints need from a robot.
type Media interface {
	CameraLast() ([]byte, time.Time)
	CameraWatch() (<-chan []byte, func())
	CameraFresh(ctx context.Context, maxAge time.Duration) ([]byte, error)
	Photo(ctx context.Context, id uint32, thumb bool) ([]byte, error)
}

func (s *Server) media(w http.ResponseWriter, r *http.Request) (Media, bool) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	rb, ok := s.robot(r.PathValue("esn")).(Media)
	if !ok {
		http.Error(w, "unknown robot", http.StatusNotFound)
		return nil, false
	}
	return rb, true
}

func writeJPEG(w http.ResponseWriter, img []byte, at time.Time) {
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	if !at.IsZero() {
		w.Header().Set("Last-Modified", at.UTC().Format(http.TimeFormat))
		w.Header().Set("X-Frame-Time", strconv.FormatInt(at.Unix(), 10))
	}
	_, _ = w.Write(img)
}

// snapshot: ?cached=true → the last frame (404 when none; never starts the camera);
// otherwise a frame at most max_age seconds old (default 5), starting the camera when needed.
func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	rb, ok := s.media(w, r)
	if !ok {
		return
	}
	if r.URL.Query().Get("cached") == "true" {
		img, at := rb.CameraLast()
		if img == nil {
			http.Error(w, "no cached frame", http.StatusNotFound)
			return
		}
		writeJPEG(w, img, at)
		return
	}
	maxAge := 5 * time.Second
	if v, err := strconv.Atoi(r.URL.Query().Get("max_age")); err == nil && v >= 0 {
		maxAge = time.Duration(v) * time.Second
	}
	img, err := rb.CameraFresh(r.Context(), maxAge)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	_, at := rb.CameraLast()
	writeJPEG(w, img, at)
}

// mjpeg streams multipart JPEG frames while the client stays connected.
func (s *Server) mjpeg(w http.ResponseWriter, r *http.Request) {
	rb, ok := s.media(w, r)
	if !ok {
		return
	}
	flusher, _ := w.(http.Flusher)
	frames, stop := rb.CameraWatch()
	defer stop()
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	w.Header().Set("Cache-Control", "no-store")
	write := func(img []byte) error {
		if _, err := fmt.Fprintf(w, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(img)); err != nil {
			return err
		}
		if _, err := w.Write(img); err != nil {
			return err
		}
		if _, err := w.Write([]byte("\r\n")); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
	// start with the cached frame so the viewer sees something while the camera starts
	if img, _ := rb.CameraLast(); img != nil {
		if write(img) != nil {
			return
		}
	}
	// a stuck camera (no frames even under behavior control) must not hang the viewer forever
	idle := time.NewTimer(mjpegIdle)
	defer idle.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-idle.C:
			s.Log.Warn("mjpeg: no camera frames, closing the stream")
			return
		case img := <-frames:
			idle.Reset(mjpegIdle)
			if write(img) != nil {
				return
			}
		}
	}
}

var mjpegIdle = 30 * time.Second

// photo returns a photo stored on the robot (?thumb=true for its thumbnail).
func (s *Server) photo(w http.ResponseWriter, r *http.Request) {
	rb, ok := s.media(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 32)
	if err != nil {
		http.Error(w, "bad photo id", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	img, err := rb.Photo(ctx, uint32(id), r.URL.Query().Get("thumb") == "true")
	if errors.Is(err, robot.ErrNoPhoto) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "max-age=86400")
	writeJPEG(w, img, time.Time{})
}

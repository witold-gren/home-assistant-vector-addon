package api

import (
	"net/http"
	"strconv"
	"time"
)

// StreamInfo tells the integration where the live stream is: RTSP on the add-on's host, user
// "vector" and the API token as the password.
type StreamInfo struct {
	RTSPPort int    `json:"rtsp_port"`
	Path     string `json:"path"`
	Username string `json:"username"`
	Audio    bool   `json:"audio"`
}

// Listener is what the microphone endpoint needs from a robot.
type Listener interface {
	MicWatch() (<-chan []byte, func())
}

// micGap: when the robot sends nothing for this long, silence is written instead, so the audio
// keeps a steady clock (ffmpeg and the browser player stall on a silent input otherwise).
var micGap = 200 * time.Millisecond

// mic streams the robot's microphone as raw 16-bit mono PCM (audio/L16) while the client stays
// connected. ?rate= is the sample rate the reader assumes (default 16000), used for the silence.
func (s *Server) mic(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r, false) {
		return
	}
	rb, ok := s.robot(r.PathValue("esn")).(Listener)
	if !ok {
		http.Error(w, "unknown robot", http.StatusNotFound)
		return
	}
	rate := 16000
	if v, err := strconv.Atoi(r.URL.Query().Get("rate")); err == nil && v >= 4000 && v <= 48000 {
		rate = v
	}
	silence := make([]byte, 2*int(int64(rate)*int64(micGap)/int64(time.Second)))
	flusher, _ := w.(http.Flusher)
	chunks, stop := rb.MicWatch()
	defer stop()
	w.Header().Set("Content-Type", "audio/L16;rate="+strconv.Itoa(rate)+";channels=1")
	w.Header().Set("Cache-Control", "no-store")
	write := func(b []byte) bool {
		if _, err := w.Write(b); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}
	gap := time.NewTimer(micGap)
	defer gap.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-gap.C:
			if !write(silence) {
				return
			}
			gap.Reset(micGap)
		case b := <-chunks:
			if !write(b) {
				return
			}
			gap.Reset(micGap)
		}
	}
}

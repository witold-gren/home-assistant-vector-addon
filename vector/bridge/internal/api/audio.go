package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

const maxAudioBytes = 50 << 20

// Speaker is what the audio endpoint needs from a robot.
type Speaker interface {
	PlayAudio(data []byte, volume int, interrupt bool) (int, error)
}

// audio: POST /audio[/<esn>]?volume=0-100&interrupt=true with an audio file (any format ffmpeg
// reads) as the body. Queued after what plays unless interrupt; answers 202 at once.
func (s *Server) audio(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rb, ok := s.robot(r.PathValue("esn")).(Speaker)
	if !ok {
		http.Error(w, "unknown robot", http.StatusNotFound)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAudioBytes))
	if err != nil {
		http.Error(w, "audio too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	volume := -1
	if v, err := strconv.Atoi(r.URL.Query().Get("volume")); err == nil {
		volume = v
	}
	queued, err := rb.PlayAudio(data, volume, r.URL.Query().Get("interrupt") == "true")
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"queued": queued})
}

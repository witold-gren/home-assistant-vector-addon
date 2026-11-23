// Package wyoming talks to a Wyoming text-to-speech server (wyoming-piper, the same server Home
// Assistant uses) straight from the bridge: `describe` lists the voices, `synthesize` returns the
// audio. Protocol: every event is one JSON header line, optionally followed by a JSON data block
// (data_length bytes) and a binary payload (payload_length bytes).
package wyoming

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"
)

const protocolVersion = "1.5.0"

// maxAudio bounds what one sentence may synthesize to (about 5 minutes at 22 kHz, 16-bit mono).
const maxAudio = 16 << 20

// Client of one Wyoming TTS server.
type Client struct {
	Addr  string // host:port
	Voice string // default voice ("" = the server's default)
	// Timeout bounds one request (connect, synthesize, read the audio).
	Timeout time.Duration
}

// New parses "tcp://host:port" or "host:port" (port 10200 when missing).
func New(rawURL, voice string) (*Client, error) {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return nil, errors.New("empty Piper address")
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("piper_url: %w", err)
		}
		if u.Scheme != "tcp" {
			return nil, fmt.Errorf("piper_url: scheme %q (use tcp://host:port)", u.Scheme)
		}
		s = u.Host
	}
	if _, _, err := net.SplitHostPort(s); err != nil {
		s = net.JoinHostPort(strings.Trim(s, "[]"), "10200")
	}
	return &Client{Addr: s, Voice: strings.TrimSpace(voice), Timeout: 30 * time.Second}, nil
}

// Event is one Wyoming message.
type Event struct {
	Type    string
	Data    map[string]any
	Payload []byte
}

type header struct {
	Type          string         `json:"type"`
	Version       string         `json:"version,omitempty"`
	Data          map[string]any `json:"data,omitempty"`
	DataLength    int            `json:"data_length,omitempty"`
	PayloadLength int            `json:"payload_length,omitempty"`
}

type conn struct {
	c    net.Conn
	r    *bufio.Reader
	stop func() bool
}

func (cl *Client) dial(ctx context.Context) (*conn, error) {
	timeout := cl.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	d := net.Dialer{Timeout: 3 * time.Second}
	c, err := d.DialContext(ctx, "tcp", cl.Addr)
	if err != nil {
		return nil, fmt.Errorf("piper: %w", err)
	}
	deadline := time.Now().Add(timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = c.SetDeadline(deadline)
	// a cancelled request closes the connection (unblocks reads)
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	return &conn{c: c, r: bufio.NewReaderSize(c, 64<<10), stop: stop}, nil
}

func (c *conn) close() {
	c.stop()
	_ = c.c.Close()
}

func (c *conn) write(typ string, data map[string]any) error {
	h := header{Type: typ, Version: protocolVersion}
	var body []byte
	if len(data) > 0 {
		var err error
		if body, err = json.Marshal(data); err != nil {
			return err
		}
		h.DataLength = len(body)
	}
	line, err := json.Marshal(h)
	if err != nil {
		return err
	}
	_, err = c.c.Write(append(append(line, '\n'), body...))
	return err
}

func (c *conn) read() (*Event, error) {
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var h header
	if err := json.Unmarshal(line, &h); err != nil {
		return nil, fmt.Errorf("piper: bad header: %w", err)
	}
	if h.DataLength < 0 || h.DataLength > 1<<20 || h.PayloadLength < 0 || h.PayloadLength > maxAudio {
		return nil, errors.New("piper: event too large")
	}
	ev := &Event{Type: h.Type, Data: h.Data}
	if h.DataLength > 0 {
		b := make([]byte, h.DataLength)
		if _, err := io.ReadFull(c.r, b); err != nil {
			return nil, err
		}
		extra := map[string]any{}
		if err := json.Unmarshal(b, &extra); err != nil {
			return nil, fmt.Errorf("piper: bad data: %w", err)
		}
		if ev.Data == nil {
			ev.Data = extra
		} else {
			for k, v := range extra {
				ev.Data[k] = v
			}
		}
	}
	if h.PayloadLength > 0 {
		ev.Payload = make([]byte, h.PayloadLength)
		if _, err := io.ReadFull(c.r, ev.Payload); err != nil {
			return nil, err
		}
	}
	return ev, nil
}

// until reads events up to one of the wanted types; an `error` event becomes a Go error.
func (c *conn) until(types ...string) (*Event, error) {
	for {
		ev, err := c.read()
		if err != nil {
			return nil, err
		}
		if ev.Type == "error" {
			return nil, fmt.Errorf("piper: %v", ev.Data["text"])
		}
		for _, t := range types {
			if ev.Type == t {
				return ev, nil
			}
		}
	}
}

// Voice offered by the server.
type Voice struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Languages   []string `json:"languages"`
	Speakers    []string `json:"speakers,omitempty"`
}

// Voices asks the server (`describe`) for its installed voices.
func (cl *Client) Voices(ctx context.Context) ([]Voice, error) {
	c, err := cl.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer c.close()
	if err := c.write("describe", nil); err != nil {
		return nil, err
	}
	ev, err := c.until("info")
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(ev.Data)
	var info struct {
		TTS []struct {
			Voices []struct {
				Name        string   `json:"name"`
				Description string   `json:"description"`
				Languages   []string `json:"languages"`
				Installed   *bool    `json:"installed"`
				Speakers    []struct {
					Name string `json:"name"`
				} `json:"speakers"`
			} `json:"voices"`
		} `json:"tts"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, fmt.Errorf("piper: bad info: %w", err)
	}
	voices := []Voice{}
	for _, p := range info.TTS {
		for _, v := range p.Voices {
			if v.Installed != nil && !*v.Installed {
				continue
			}
			vc := Voice{Name: v.Name, Description: v.Description, Languages: v.Languages}
			if vc.Languages == nil {
				vc.Languages = []string{}
			}
			for _, s := range v.Speakers {
				vc.Speakers = append(vc.Speakers, s.Name)
			}
			voices = append(voices, vc)
		}
	}
	return voices, nil
}

// Format of raw PCM audio.
type Format struct {
	Rate, Width, Channels int
}

func intOf(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

func formatOf(d map[string]any) Format {
	return Format{Rate: intOf(d["rate"]), Width: intOf(d["width"]), Channels: intOf(d["channels"])}
}

// Synthesize turns text into a WAV file with `voice` ("" = the client's default voice).
func (cl *Client) Synthesize(ctx context.Context, text, voice string) ([]byte, error) {
	if voice == "" {
		voice = cl.Voice
	}
	c, err := cl.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer c.close()
	data := map[string]any{"text": text}
	if voice != "" {
		data["voice"] = map[string]any{"name": voice}
	}
	if err := c.write("synthesize", data); err != nil {
		return nil, err
	}
	start, err := c.until("audio-start")
	if err != nil {
		return nil, err
	}
	f := formatOf(start.Data)
	var pcm []byte
	for {
		ev, err := c.until("audio-chunk", "audio-stop")
		if err != nil {
			return nil, err
		}
		if ev.Type == "audio-stop" {
			break
		}
		if cf := formatOf(ev.Data); cf.Rate > 0 {
			f = cf
		}
		if len(pcm)+len(ev.Payload) > maxAudio {
			return nil, errors.New("piper: audio too long")
		}
		pcm = append(pcm, ev.Payload...)
	}
	if f.Rate <= 0 || f.Width <= 0 || f.Channels <= 0 {
		return nil, errors.New("piper: no audio format")
	}
	if len(pcm) == 0 {
		return nil, errors.New("piper: no audio")
	}
	return WAV(pcm, f), nil
}

// WAV wraps PCM in a RIFF header (what ffmpeg reads without being told the format).
func WAV(pcm []byte, f Format) []byte {
	var b bytes.Buffer
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + len(pcm)))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1))
	w(uint16(f.Channels))
	w(uint32(f.Rate))
	w(uint32(f.Rate * f.Width * f.Channels))
	w(uint16(f.Width * f.Channels))
	w(uint16(8 * f.Width))
	b.WriteString("data")
	w(uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes()
}

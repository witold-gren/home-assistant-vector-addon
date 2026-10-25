package robot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

// Vector plays 16-bit little-endian mono PCM through ExternalAudioStreamPlayback (no behavior
// control needed — FINDINGS.md). Chunks are at most 1024 bytes and are sent at real-time pace, at most
// `audioLead` ahead of the speaker, so a stop takes effect quickly.
const (
	audioRate  = 16000
	audioChunk = 1024
	maxClipLen = 10 * time.Minute
)

var audioLead = time.Second

// Decoder turns any audio file into PCM s16le mono at audioRate (ffmpeg; replaced in tests).
type Decoder func(ctx context.Context, data []byte) ([]byte, error)

// FFmpegDecode decodes with the ffmpeg binary.
func FFmpegDecode(ctx context.Context, data []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-i", "pipe:0",
		"-t", fmt.Sprint(int(maxClipLen.Seconds())), "-f", "s16le", "-ac", "1", "-ar", fmt.Sprint(audioRate), "pipe:1")
	cmd.Stdin = bytes.NewReader(data)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	return out.Bytes(), nil
}

type clip struct {
	data   []byte
	volume int
}

// Player plays clips one after another on the robot speaker.
type Player struct {
	m      *Manager
	decode Decoder

	mu      sync.Mutex
	queue   []clip
	running bool
	cancel  context.CancelFunc // current clip
	volume  int
}

func newPlayer(m *Manager, decode Decoder) *Player {
	if decode == nil {
		decode = FFmpegDecode
	}
	return &Player{m: m, decode: decode, volume: 80}
}

// Play queues an audio file (any format ffmpeg reads). volume < 0 uses the player volume;
// interrupt drops what plays and what waits (announcements). Returns the queue length.
func (p *Player) Play(data []byte, volume int, interrupt bool) (int, error) {
	if _, err := p.m.client(); err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, errors.New("empty audio")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if volume < 0 {
		volume = p.volume
	}
	volume = min(volume, 100)
	if interrupt {
		p.queue = nil
		if p.cancel != nil {
			p.cancel()
		}
	}
	p.queue = append(p.queue, clip{data: data, volume: volume})
	if !p.running {
		p.running = true
		go p.loop()
	}
	return len(p.queue), nil
}

// Stop drops the queue and cuts the current clip.
func (p *Player) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queue = nil
	if p.cancel != nil {
		p.cancel()
	}
}

// SetVolume sets the default clip volume (0–100).
func (p *Player) SetVolume(v int) {
	v = max(0, min(100, v))
	p.mu.Lock()
	p.volume = v
	p.mu.Unlock()
	p.m.update(func(t *Tracker) bool {
		if t.S.Audio.Volume == v {
			return false
		}
		t.S.Audio.Volume = v
		return true
	})
}

func (p *Player) loop() {
	for {
		p.mu.Lock()
		if len(p.queue) == 0 {
			p.running = false
			p.mu.Unlock()
			return
		}
		c := p.queue[0]
		p.queue = p.queue[1:]
		ctx, cancel := context.WithCancel(context.Background())
		p.cancel = cancel
		p.mu.Unlock()

		err := p.playClip(ctx, c)
		cancel()
		p.mu.Lock()
		p.cancel = nil
		p.mu.Unlock()
		if err != nil && !errors.Is(err, context.Canceled) {
			p.m.log.Warn("audio playback failed", "err", err)
		}
	}
}

func (p *Player) setPlaying(playing bool, duration float64) {
	p.m.update(func(t *Tracker) bool {
		t.S.Audio.Playing, t.S.Audio.Duration = playing, duration
		if playing {
			t.S.Audio.Started = time.Now().Unix()
		}
		return true
	})
}

func (p *Player) playClip(ctx context.Context, c clip) error {
	dctx, dcancel := context.WithTimeout(ctx, time.Minute)
	pcm, err := p.decode(dctx, c.data)
	dcancel()
	if err != nil {
		return err
	}
	if len(pcm) == 0 {
		return errors.New("no audio after decoding")
	}
	cli, err := p.m.client()
	if err != nil {
		return err
	}
	esn := p.m.ESN()
	duration := float64(len(pcm)) / (2 * audioRate)
	p.setPlaying(true, duration)
	p.m.sink.Event(esn, Event{Name: "audio_started", Data: map[string]any{"duration": duration}})
	err = stream(ctx, cli, pcm, c.volume)
	p.setPlaying(false, 0)
	p.m.sink.Event(esn, Event{Name: "audio_finished", Data: map[string]any{"interrupted": err != nil}})
	return err
}

// stream sends pcm at real-time pace and waits for the robot to finish (or cancels on ctx).
func stream(ctx context.Context, cli vectorpb.ExternalInterfaceClient, pcm []byte, volume int) error {
	sctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := cli.ExternalAudioStreamPlayback(sctx)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		for {
			r, err := st.Recv()
			if err != nil {
				done <- err
				return
			}
			switch {
			case r.GetAudioStreamPlaybackComplete() != nil:
				done <- nil
				return
			case r.GetAudioStreamPlaybackFailyer() != nil:
				done <- errors.New("robot: audio playback failed")
				return
			case r.GetAudioStreamBufferOverrun() != nil:
				done <- errors.New("robot: audio buffer overrun")
				return
			}
		}
	}()
	send := func(req *vectorpb.ExternalAudioStreamRequest) error { return st.Send(req) }
	if err := send(&vectorpb.ExternalAudioStreamRequest{AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamPrepare{
		AudioStreamPrepare: &vectorpb.ExternalAudioStreamPrepare{AudioFrameRate: audioRate, AudioVolume: uint32(volume)}}}); err != nil {
		return err
	}
	cancelled := func() error {
		_ = send(&vectorpb.ExternalAudioStreamRequest{AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamCancel{
			AudioStreamCancel: &vectorpb.ExternalAudioStreamCancel{}}})
		time.Sleep(100 * time.Millisecond) // let the cancel reach the robot before closing the stream
		return ctx.Err()
	}
	start := time.Now()
	bytesPerSec := float64(2 * audioRate)
	for off := 0; off < len(pcm); off += audioChunk {
		ahead := time.Duration(float64(off)/bytesPerSec*float64(time.Second)) - time.Since(start)
		if ahead > audioLead {
			select {
			case <-ctx.Done():
				return cancelled()
			case <-time.After(ahead - audioLead):
			}
		}
		if ctx.Err() != nil {
			return cancelled()
		}
		end := min(off+audioChunk, len(pcm))
		if err := send(&vectorpb.ExternalAudioStreamRequest{AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamChunk{
			AudioStreamChunk: &vectorpb.ExternalAudioStreamChunk{AudioChunkSizeBytes: uint32(end - off), AudioChunkSamples: pcm[off:end]}}}); err != nil {
			return err
		}
	}
	if err := send(&vectorpb.ExternalAudioStreamRequest{AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamComplete{
		AudioStreamComplete: &vectorpb.ExternalAudioStreamComplete{}}}); err != nil {
		return err
	}
	remaining := time.Duration(float64(len(pcm))/bytesPerSec*float64(time.Second)) - time.Since(start)
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return cancelled()
	case <-time.After(remaining + 10*time.Second):
		return errors.New("robot: no playback-complete")
	}
}

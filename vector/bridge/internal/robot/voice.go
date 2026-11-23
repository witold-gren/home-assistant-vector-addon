package robot

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/wyoming"
)

// TTS is a Wyoming text-to-speech server (Piper) the bridge talks to directly.
type TTS interface {
	Voices(ctx context.Context) ([]wyoming.Voice, error)
	Synthesize(ctx context.Context, text, voice string) ([]byte, error) // a WAV file
}

// Effect changes a WAV file (a funny voice); "none" returns it unchanged.
type Effect func(ctx context.Context, wav []byte, effect string) ([]byte, error)

const (
	maxVoiceText = 300
	ttsTimeout   = 20 * time.Second
)

// Effects a program may ask for (say_voice `effect`).
var Effects = []string{"none", "mouse", "giant", "robot", "echo", "fast", "slow"}

var voiceNameRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,100}$`)

// effectFilter: the ffmpeg audio filter of an effect for audio at `rate` Hz.
func effectFilter(effect string, rate int) (string, error) {
	if rate <= 0 {
		rate = 22050
	}
	switch effect {
	case "mouse": // higher and a bit faster
		return fmt.Sprintf("asetrate=%d,aresample=%d,atempo=0.85", rate*3/2, rate), nil
	case "giant": // lower and slower
		return fmt.Sprintf("asetrate=%d,aresample=%d,atempo=1.2", rate*7/10, rate), nil
	case "robot":
		return "afftfilt=real='hypot(re,im)':imag='0':win_size=512:overlap=0.75,volume=1.5", nil
	case "echo":
		return "aecho=0.8:0.85:120|240:0.45|0.25", nil
	case "fast":
		return "atempo=1.4", nil
	case "slow":
		return "atempo=0.75", nil
	}
	return "", fmt.Errorf("effect: one of %s", strings.Join(Effects, ", "))
}

// wavRate reads the sample rate of a WAV header (0 when not a plain WAV).
func wavRate(wav []byte) int {
	if len(wav) < 44 || string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return 0
	}
	return int(binary.LittleEndian.Uint32(wav[24:28]))
}

// FFmpegEffect applies an effect with the ffmpeg binary.
func FFmpegEffect(ctx context.Context, wav []byte, effect string) ([]byte, error) {
	if effect == "" || effect == "none" {
		return wav, nil
	}
	filter, err := effectFilter(effect, wavRate(wav))
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-i", "pipe:0",
		"-af", filter, "-f", "wav", "pipe:1")
	cmd.Stdin = bytes.NewReader(wav)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg effect: %v: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	return out.Bytes(), nil
}

// listVoices: the Piper voices (Wyoming describe).
func (m *Manager) listVoices(ctx context.Context) (any, error) {
	if m.cfg.TTS == nil {
		return nil, errors.New("no own voices (set the add-on option piper_url)")
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	v, err := m.cfg.TTS.Voices(cctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"voices": v, "effects": Effects}, nil
}

// sayVoice speaks text with a Piper voice (and an effect) through the bridge's own player; without
// Piper, or when Piper fails, wire-pod's say_text speaks it with wire-pod's voice.
func (m *Manager) sayVoice(ctx context.Context, a Args) (any, error) {
	text := strings.Join(strings.Fields(a.str("text", "")), " ")
	if text == "" {
		return nil, errors.New("text required")
	}
	if len([]rune(text)) > maxVoiceText {
		return nil, fmt.Errorf("text too long (max %d characters)", maxVoiceText)
	}
	voice := strings.TrimSpace(a.str("voice", ""))
	if voice != "" && !voiceNameRe.MatchString(voice) {
		return nil, errors.New("voice: a voice name from list_voices")
	}
	effect := a.str("effect", "none")
	if effect != "none" {
		if _, err := effectFilter(effect, 16000); err != nil {
			return nil, err
		}
	}
	wait := a.boolean("wait", true)
	volume := -1
	if a.has("volume") {
		volume = int(clamp(a.num("volume", 80), 0, 100))
	}

	if m.cfg.TTS == nil {
		return m.sayFallback(ctx, text, "")
	}
	sctx, cancel := context.WithTimeout(ctx, ttsTimeout)
	wav, err := m.cfg.TTS.Synthesize(sctx, text, voice)
	if err == nil && effect != "none" {
		wav, err = m.cfg.Effect(sctx, wav, effect)
	}
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		m.log.Warn("piper failed, wire-pod speaks instead", "err", err)
		return m.sayFallback(ctx, text, err.Error())
	}
	if !wait {
		queued, err := m.Player.Play(wav, volume, false)
		if err != nil {
			return nil, err
		}
		return map[string]any{"engine": "piper", "queued": queued}, nil
	}
	d, err := m.Player.PlayWait(ctx, wav, volume)
	if errors.Is(err, context.Canceled) && ctx.Err() == nil {
		return nil, errors.New("stopped")
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"engine": "piper", "duration": d}, nil
}

// sayFallback: wire-pod's say_text (its voice, no effect); returns after speaking.
func (m *Manager) sayFallback(ctx context.Context, text, why string) (any, error) {
	if m.cfg.Wirepod == nil {
		if why != "" {
			return nil, fmt.Errorf("piper: %s", why)
		}
		return nil, errors.New("no voice: set piper_url or wirepod_url")
	}
	if err := m.cfg.Wirepod.SayText(ctx, m.ESN(), text); err != nil {
		return nil, err
	}
	res := map[string]any{"engine": "wirepod", "fallback": true}
	if why != "" {
		res["error"] = why
	}
	return res, nil
}

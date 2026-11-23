package robot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"

	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/ai"
	hass "github.com/witold-gren/home-assistant-vector-addon/bridge/internal/ha"
)

// Asker answers a child's question (a local LLM; see package ai).
type Asker interface {
	Ask(ctx context.Context, question string, maxWords int) (ai.Answer, error)
}

// Weather reads the weather from Home Assistant (see package ha).
type Weather interface {
	Weather(ctx context.Context, entity string, days int) (hass.Weather, error)
}

func jsonRaw(v any) (json.RawMessage, error) { return json.Marshal(v) }

// v5Command runs the commands of stage V5 (voices, listening, behaviors, AI, weather, volume,
// mirror mode, photo). ok=false: not one of them.
func (m *Manager) v5Command(ctx context.Context, name string, a Args) (res any, err error, ok bool) {
	ok = true
	switch name {
	case "list_voices":
		res, err = m.listVoices(ctx)
	case "say_voice":
		res, err = m.sayVoice(ctx, a)
	case "listen":
		res, err = m.listen(ctx, a)
	case "run_behavior":
		res, err = m.runBehavior(ctx, a)
	case "ask_ai":
		if m.cfg.AI == nil {
			return nil, ai.ErrOff, true
		}
		var ans ai.Answer
		ans, err = m.cfg.AI.Ask(ctx, a.str("question", ""), int(a.num("max_words", ai.DefaultMaxWords)))
		if err == nil {
			res = ans
		}
	case "weather":
		if m.cfg.Weather == nil {
			return nil, hass.ErrNoHA, true
		}
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		var w hass.Weather
		w, err = m.cfg.Weather.Weather(cctx, a.str("entity", ""), int(clamp(a.num("forecast_days", 0), 0, 5)))
		cancel()
		if err == nil {
			res = w
		}
	case "set_volume":
		err = m.sessionVolume(ctx, int(clamp(a.num("level", 3), 0, 5)))
	case "mirror_mode":
		err = m.mirrorMode(ctx, a.boolean("enable", true))
	case "take_photo":
		res, err = m.takePhoto(ctx)
	default:
		ok = false
	}
	return res, err, ok
}

// volumeNow: master volume 0–5 from the settings document (-1 = unknown).
func (m *Manager) volumeNow() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.tr.S.Settings["master_volume"].(float64); ok {
		return int(v)
	}
	return -1
}

// sessionVolume sets the master volume for the program; the first change remembers the old one.
func (m *Manager) sessionVolume(ctx context.Context, level int) error {
	before := m.volumeNow()
	if err := m.setVolume(ctx, level); err != nil {
		return err
	}
	m.progMu.Lock()
	if p := m.prog; p != nil && p.prevVolume < 0 {
		p.prevVolume = before
		if before < 0 {
			p.prevVolume = 3 // unknown: "medium" afterwards
		}
	}
	m.progMu.Unlock()
	return nil
}

// restVolume: update_settings over the robot's REST gateway (what the integration's select does).
func (m *Manager) restVolume(ctx context.Context, level int) error {
	cli, err := m.client()
	if err != nil {
		return err
	}
	a := Args{}
	a["master_volume"], _ = jsonRaw(Volumes[max(0, min(len(Volumes)-1, level))])
	return m.updateSettings(ctx, cli, a)
}

func (m *Manager) mirrorMode(ctx context.Context, on bool) error {
	cli, err := m.client()
	if err != nil {
		return err
	}
	if _, err := cli.EnableMirrorMode(ctx, &vectorpb.EnableMirrorModeRequest{Enable: on}); err != nil {
		return err
	}
	m.progMu.Lock()
	if p := m.prog; p != nil {
		p.mirror = on
	}
	m.progMu.Unlock()
	return nil
}

// takePhoto: one fresh camera frame as a data URL — shown in the studio, stored nowhere (not on
// the robot, not in the bridge beyond the usual last-frame cache).
func (m *Manager) takePhoto(ctx context.Context) (any, error) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	jpg, err := m.Camera.Fresh(cctx, 0)
	if err != nil {
		return nil, err
	}
	if len(jpg) == 0 {
		return nil, errors.New("no camera frame")
	}
	_, at := m.Camera.Last()
	return map[string]any{"image": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpg),
		"bytes": len(jpg), "at": at.Unix()}, nil
}

// restoreSession gives back what the ended session changed: speech to Home Assistant, a waiting
// listen, mirror mode, the master volume. In the background: the robot may be gone already.
func (m *Manager) restoreSession(p *program) {
	m.cancelListen()
	if p.claim {
		if f := m.claimHook(); f != nil {
			f(false)
		}
	}
	mirror, vol := p.mirror, p.prevVolume
	if !mirror && vol < 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if mirror {
			if cli, err := m.client(); err == nil {
				if _, err := cli.EnableMirrorMode(ctx, &vectorpb.EnableMirrorModeRequest{Enable: false}); err != nil {
					m.log.Warn("program end: mirror mode off failed", "err", err)
				}
			}
		}
		if vol >= 0 {
			if err := m.setVolume(ctx, vol); err != nil {
				m.log.Warn("program end: volume not restored", "err", err)
			}
		}
	}()
}

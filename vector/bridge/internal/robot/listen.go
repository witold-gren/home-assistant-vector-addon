package robot

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Speech for programs. Vector sends what it hears after "Hey Vector" only to its own server
// (wire-pod); wire-pod's system intent `ha_router` hands every sentence to the bridge's voice router.
// A program that began with claim_speech gets those sentences instead of Home Assistant: the router
// answers wire-pod "handled, stay silent" at once and gives the text to the session (sensors
// last_speech + counts.speech, and a waiting `listen`). Nothing of it is broadcast to other clients:
// the program_heard event carries only the length.

var (
	ErrSpeechNotClaimed = errors.New("listening needs program_begin with claim_speech")
	ErrAlreadyListening = errors.New("already listening")
)

const (
	listenDefault = 8.0
	listenMin     = 2.0
	listenMax     = 30.0
	maxPrompt     = 200
)

// SetClaimHook: f(true) when a program claims speech, f(false) when it ends (the voice router
// keeps its wire-pod intent installed meanwhile, even when switched off for Home Assistant).
func (m *Manager) SetClaimHook(f func(on bool)) {
	m.claimMu.Lock()
	m.onClaim = f
	m.claimMu.Unlock()
}

func (m *Manager) claimHook() func(bool) {
	m.claimMu.Lock()
	defer m.claimMu.Unlock()
	return m.onClaim
}

// SpeechClaimed: a running program takes the sentences of robot esn ("" = any).
func (m *Manager) SpeechClaimed(esn string) bool {
	if esn != "" && !strings.EqualFold(esn, m.ESN()) {
		return false
	}
	m.progMu.Lock()
	defer m.progMu.Unlock()
	return m.prog != nil && !m.prog.starting && m.prog.claim
}

// TakeSpeech gives a sentence to the program when it claims speech (true = taken: wire-pod must
// stay silent and Home Assistant never sees it).
func (m *Manager) TakeSpeech(esn, text string) bool {
	if !m.SpeechClaimed(esn) {
		return false
	}
	m.NoteSpeech(text)
	m.listenMu.Lock()
	if m.listener != nil {
		select {
		case m.listener <- text:
		default:
		}
	}
	m.listenMu.Unlock()
	m.sink.Event(m.ESN(), Event{Name: "program_heard", Data: map[string]any{"chars": len([]rune(text))}})
	return true
}

// listen waits for the next sentence (after "Hey Vector", or after the robot was made to listen
// with wake: true). Optional prompt: spoken first (say_voice rules).
func (m *Manager) listen(ctx context.Context, a Args) (any, error) {
	if !m.SpeechClaimed("") {
		return nil, ErrSpeechNotClaimed
	}
	timeout := time.Duration(clamp(a.num("timeout", listenDefault), listenMin, listenMax) * float64(time.Second))
	prompt := strings.TrimSpace(a.str("prompt", ""))
	if len([]rune(prompt)) > maxPrompt {
		return nil, errors.New("prompt too long (max 200 characters)")
	}

	ch := make(chan string, 1)
	m.listenMu.Lock()
	if m.listener != nil {
		m.listenMu.Unlock()
		return nil, ErrAlreadyListening
	}
	m.listener = ch
	m.listenMu.Unlock()
	defer func() {
		m.listenMu.Lock()
		if m.listener == ch {
			m.listener = nil
		}
		m.listenMu.Unlock()
	}()

	if prompt != "" {
		pa := Args{}
		pa["text"], _ = jsonRaw(prompt)
		pa["wait"], _ = jsonRaw(true)
		if v := a.str("voice", ""); v != "" {
			pa["voice"], _ = jsonRaw(v)
		}
		if _, err := m.sayVoice(ctx, pa); err != nil {
			return nil, err
		}
		// what was said while the prompt played does not count
		select {
		case <-ch:
		default:
		}
	}
	woke := false
	if a.boolean("wake", false) {
		woke = m.triggerWakeWord(ctx)
	}

	start := time.Now()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case text := <-ch:
		return map[string]any{"heard": true, "text": text, "waited": round(time.Since(start).Seconds(), 0.1), "woke": woke}, nil
	case <-timer.C:
		return map[string]any{"heard": false, "text": "", "waited": round(time.Since(start).Seconds(), 0.1), "woke": woke}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-m.sessionGone():
		return nil, errors.New("program ended")
	}
}

// triggerWakeWord makes Vector listen as if "Hey Vector" was said (wire-pod's trigger_wake_word,
// which uses the robot's debug console — may not work on production firmware).
func (m *Manager) triggerWakeWord(ctx context.Context) bool {
	if m.cfg.Wirepod == nil {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	if err := m.cfg.Wirepod.SDKCall(cctx, "/api-sdk/trigger_wake_word", m.ESN()); err != nil {
		m.log.Info("trigger_wake_word failed (say \"Hey Vector\" instead)", "err", err)
		return false
	}
	return true
}

// sessionGone closes when no claiming program runs any more (checked every 200 ms).
func (m *Manager) sessionGone() <-chan struct{} {
	m.progMu.Lock()
	p := m.prog
	m.progMu.Unlock()
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for range t.C {
			m.progMu.Lock()
			gone := m.prog != p
			m.progMu.Unlock()
			if gone {
				close(done)
				return
			}
			m.listenMu.Lock()
			idle := m.listener == nil
			m.listenMu.Unlock()
			if idle {
				return
			}
		}
	}()
	return done
}

// cancelListen ends a waiting `listen` (program end).
func (m *Manager) cancelListen() {
	m.listenMu.Lock()
	m.listener = nil
	m.listenMu.Unlock()
}

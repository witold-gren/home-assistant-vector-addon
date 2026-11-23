package robot

import (
	"context"
	"errors"
	"time"
)

// Stand still: the bridge keeps behavior control between programs, so Vector does not roam, turn
// or fidget (children program him, a parent tests). A program takes over as usual; when it ends
// the bridge keeps the same control stream (no gap in which Vector could drive away) unless the
// program ended with go_to_charger. `stop` and release_control (the card after driving) keep it.
// Vector is let go by `stay_still {on:false}`, a low battery, `StayStillTimeout` without a program, go_to_charger and the add-on stopping.
// When Vector takes control back (picked up, a cliff) or the robot reconnects, the keeper asks
// for it again every `stayRetry`.

// On the charger standing still is allowed: Vector charges by the contacts while the SDK holds
// control (he only cannot drive home on his own — so a low battery lets him go).
var errStayNotConnected = errors.New("robot not connected")

const stayHoldFor = 24 * time.Hour // the lease's hold; the keeper and the idle timer end it earlier

// variables: tests use shorter ones
var (
	stayRetry          = 5 * time.Second
	stayTimeoutDefault = 60 * time.Minute
)

type stayState struct {
	on      bool
	idle    *time.Timer // no program for StayStillTimeout → let go
	keeping bool        // the keeper goroutine runs
	stop    chan struct{}
}

func (m *Manager) stayTimeout() time.Duration {
	if m.cfg.StayStillTimeout > 0 {
		return m.cfg.StayStillTimeout
	}
	return stayTimeoutDefault
}

// StayStill reports whether the bridge keeps Vector still between programs.
func (m *Manager) StayStill() bool {
	m.stayMu.Lock()
	defer m.stayMu.Unlock()
	return m.stay.on
}

// stayCommand: `stay_still {on}` → {stay_still}.
func (m *Manager) stayCommand(ctx context.Context, a Args) (any, error) {
	if !a.has("on") {
		return map[string]any{"stay_still": m.StayStill()}, nil
	}
	if a.boolean("on", false) {
		if err := m.stayOn(ctx); err != nil {
			return nil, err
		}
	} else {
		m.stayOff("off")
	}
	return map[string]any{"stay_still": m.StayStill()}, nil
}

// stayOn starts standing still: control now (unless a program holds it already), the idle timer
// and the keeper.
func (m *Manager) stayOn(ctx context.Context) error {
	if _, err := m.client(); err != nil {
		return errStayNotConnected
	}
	if b := m.Snapshot().Battery; b != nil && (b.Level == "low" || b.Percent < lowBatteryPercent) {
		return ErrBatteryLow
	}
	m.stayMu.Lock()
	was := m.stay.on
	m.stay.on = true
	m.armIdleLocked()
	if !m.stay.keeping {
		m.stay.keeping = true
		m.stay.stop = make(chan struct{})
		go m.stayKeeper(m.stay.stop)
	}
	m.stayMu.Unlock()
	if !m.programActive() {
		hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := m.holdStill(hctx)
		cancel()
		if err != nil {
			m.log.Info("stand still: control not granted yet, the keeper retries", "err", err)
		}
	}
	m.setStayState(true)
	if !was {
		m.log.Info("stand still on", "timeout", m.stayTimeout())
	}
	return nil
}

// stayOff lets Vector go (reason: off | battery_low | timeout | charger).
func (m *Manager) stayOff(reason string) {
	m.stayMu.Lock()
	was := m.stay.on
	m.stay.on = false
	if m.stay.idle != nil {
		m.stay.idle.Stop()
		m.stay.idle = nil
	}
	if m.stay.keeping {
		close(m.stay.stop)
		m.stay.keeping = false
	}
	m.stayMu.Unlock()
	if !was {
		return
	}
	if !m.programActive() {
		m.lease.Release()
	}
	m.setStayState(false)
	m.log.Info("stand still off", "reason", reason)
	if reason != "off" {
		m.sink.Event(m.ESN(), Event{Name: "stay_still_ended", Data: map[string]any{"reason": reason}})
	}
}

func (m *Manager) setStayState(on bool) {
	m.update(func(t *Tracker) bool {
		if t.S.StayStill == on {
			return false
		}
		t.S.StayStill = on
		return true
	})
}

// armIdleLocked (stayMu held): StayStillTimeout from now without a program → let go.
func (m *Manager) armIdleLocked() {
	if m.stay.idle != nil {
		m.stay.idle.Stop()
	}
	m.stay.idle = time.AfterFunc(m.stayTimeout(), func() {
		if m.programActive() {
			m.stayMu.Lock()
			if m.stay.on {
				m.armIdleLocked()
			}
			m.stayMu.Unlock()
			return
		}
		m.stayOff("timeout")
	})
}

func (m *Manager) programActive() bool {
	m.progMu.Lock()
	defer m.progMu.Unlock()
	return m.prog != nil
}

// holdStill takes (or keeps) control for standing still.
func (m *Manager) holdStill(ctx context.Context) error {
	return m.lease.Hold(ctx, stayHoldFor)
}

// keepStillAfterProgram (endProgram): the program's control stream simply stays — Hold on a
// granted stream returns at once, so there is no moment without control. False = let go.
func (m *Manager) keepStillAfterProgram(reason string) bool {
	if reason == "charger" {
		m.stayOff("charger")
		return false
	}
	m.stayMu.Lock()
	on := m.stay.on
	if on {
		m.armIdleLocked()
	}
	m.stayMu.Unlock()
	if !on {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.holdStill(ctx); err != nil {
		m.log.Info("stand still after the program: control not kept, the keeper retries", "reason", reason, "err", err)
	}
	return true
}

// stayKeeper: low battery → let go; control lost or reconnected → ask again.
func (m *Manager) stayKeeper(stop <-chan struct{}) {
	t := time.NewTicker(stayRetry)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		st := m.Snapshot()
		if b := st.Battery; b != nil && (b.Level == "low" || b.Percent < lowBatteryPercent) {
			m.stayOff("battery_low")
			return
		}
		if !st.Connected || m.programActive() || st.Control == "held" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := m.holdStill(ctx); err != nil {
			m.log.Debug("stand still: control not back yet", "err", err)
		}
		cancel()
		// switched off while we asked: give it back (stayOff released before the grant came)
		if !m.StayStill() && !m.programActive() {
			m.lease.Release()
			return
		}
	}
}

package robot

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

// Vector's own behaviors in a program (run_behavior). The robot runs an app intent only when it
// has control itself, so the session suspends control: the bridge lets go (the program lock stays:
// nobody else may drive; the watchdog pauses), sends the intent, watches the robot until the
// behavior ends or the time is up, and takes control back. go_to_charger ends the program instead:
// the robot finds its charger itself and must keep control to dock.

// Behavior of the white list.
type Behavior struct {
	Intent      string  // AppIntent
	Timeout     float64 // default seconds
	EndsProgram bool
}

// Behaviors programs may run (app intents the firmware accepts from the SDK).
var Behaviors = map[string]Behavior{
	"dance":           {Intent: "intent_imperative_dance", Timeout: 30},
	"come_here":       {Intent: "intent_imperative_come", Timeout: 30},
	"look_at_me":      {Intent: "intent_imperative_lookatme", Timeout: 15},
	"look_over_there": {Intent: "intent_imperative_lookoverthere", Timeout: 15},
	"who_am_i":        {Intent: "intent_names_ask", Timeout: 20},
	"find_cube":       {Intent: "intent_imperative_findcube", Timeout: 45},
	"fetch_cube":      {Intent: "intent_imperative_fetchcube", Timeout: 60},
	"explore":         {Intent: "explore_start", Timeout: 60},
	"go_to_charger":   {Intent: "intent_system_charger", EndsProgram: true},
}

// BehaviorNames sorted (for errors and docs).
func BehaviorNames() []string {
	names := make([]string, 0, len(Behaviors))
	for n := range Behaviors {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

const (
	behaviorMin = 5.0
	behaviorMax = 120.0
)

// how the end of a behavior is seen (variables: tests use shorter ones)
var (
	behaviorPoll     = 250 * time.Millisecond
	behaviorStartBy  = 6 * time.Second         // nothing happened by then: the robot did not take it
	behaviorSettle   = 1500 * time.Millisecond // calm this long after a change of activity = done
	behaviorQuietEnd = 4 * time.Second         // calm this long = done
	behaviorLetGo    = 400 * time.Millisecond  // the release reaching the robot before the intent
)

// behaviorObs: what the watcher sees of the robot at one moment.
type behaviorObs struct {
	activitySeq int  // changes of the robot's active feature
	busy        bool // moving, animating or pathing
}

// behaviorWatch decides from observations when a behavior is over.
type behaviorWatch struct {
	start     time.Time
	seq0      int
	started   bool
	startSeq  int
	lastBusy  time.Time
	changedAt bool // the activity changed again after the behavior began
}

func newBehaviorWatch(now time.Time, o behaviorObs) *behaviorWatch {
	return &behaviorWatch{start: now, seq0: o.activitySeq}
}

// step: done + reason ("finished" | "not_started").
func (w *behaviorWatch) step(now time.Time, o behaviorObs) (bool, string) {
	if !w.started {
		if o.activitySeq != w.seq0 || o.busy {
			w.started, w.startSeq, w.lastBusy = true, o.activitySeq, now
			return false, ""
		}
		if now.Sub(w.start) >= behaviorStartBy {
			return true, "not_started"
		}
		return false, ""
	}
	if o.busy {
		w.lastBusy = now
	}
	if o.activitySeq != w.startSeq {
		w.changedAt = true
	}
	calm := now.Sub(w.lastBusy)
	if w.changedAt && calm >= behaviorSettle || calm >= behaviorQuietEnd {
		return true, "finished"
	}
	return false, ""
}

// behaviorRunning: a program's run_behavior has let go of control right now.
func (m *Manager) behaviorRunning() bool {
	m.progMu.Lock()
	defer m.progMu.Unlock()
	return m.prog != nil && m.prog.behavior != ""
}

func (m *Manager) behaviorObs() behaviorObs {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.tr.live.status
	busy := has(st, vectorpb.RobotStatus_ROBOT_STATUS_IS_MOVING) || has(st, vectorpb.RobotStatus_ROBOT_STATUS_ARE_WHEELS_MOVING) ||
		has(st, vectorpb.RobotStatus_ROBOT_STATUS_IS_ANIMATING) || has(st, vectorpb.RobotStatus_ROBOT_STATUS_IS_PATHING)
	return behaviorObs{activitySeq: m.tr.live.activitySeq, busy: busy}
}

// runBehavior: see the top of this file. Result {behavior, finished, reason, seconds, control}.
func (m *Manager) runBehavior(ctx context.Context, a Args) (any, error) {
	name := a.str("behavior", "")
	b, ok := Behaviors[name]
	if !ok {
		return nil, fmt.Errorf("behavior: one of %s", strings.Join(BehaviorNames(), ", "))
	}
	cli, err := m.client()
	if err != nil {
		return nil, err
	}
	if b.EndsProgram {
		return m.behaviorEndingProgram(ctx, cli, name, b)
	}
	timeout := time.Duration(clamp(a.num("timeout", b.Timeout), behaviorMin, behaviorMax) * float64(time.Second))

	// one action at a time; `stop` / the program's end cancels the wait
	m.action.Lock()
	defer m.action.Unlock()
	bctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m.actMu.Lock()
	m.actCtl = cancel
	m.actMu.Unlock()
	defer func() {
		m.actMu.Lock()
		m.actCtl = nil
		m.actMu.Unlock()
	}()

	m.progMu.Lock()
	p := m.prog
	if p == nil || p.starting {
		m.progMu.Unlock()
		return nil, ErrSessionRequired
	}
	p.behavior = name
	p.watch.Stop()
	m.progMu.Unlock()
	m.log.Info("program: Vector behavior", "behavior", name, "timeout", timeout)

	// back to the program: the session still runs → control again, watchdog again
	resume := func() (bool, error) {
		m.progMu.Lock()
		alive := m.prog == p
		if alive {
			p.behavior = ""
		}
		m.progMu.Unlock()
		if !alive {
			return false, errors.New("program ended")
		}
		rctx, rcancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer rcancel()
		if err := m.lease.Hold(rctx, time.Until(p.since.Add(p.limit))+programHoldGrace); err != nil {
			m.endProgram(p.id, "control_lost", "behavior")
			return false, fmt.Errorf("control not back after the behavior: %w", err)
		}
		m.progMu.Lock()
		if m.prog == p {
			p.watch.Reset(p.heartbeat)
		}
		m.progMu.Unlock()
		return true, nil
	}

	m.lease.Release()
	select {
	case <-time.After(behaviorLetGo):
	case <-bctx.Done():
	}
	started := time.Now()
	reason := ""
	if bctx.Err() == nil {
		if _, err := cli.AppIntent(bctx, &vectorpb.AppIntentRequest{Intent: b.Intent}); err != nil {
			if _, rerr := resume(); rerr != nil {
				return nil, rerr
			}
			return nil, fmt.Errorf("robot: %w", err)
		}
		w := newBehaviorWatch(time.Now(), m.behaviorObs())
		tick := time.NewTicker(behaviorPoll)
		deadline := time.NewTimer(timeout)
	wait:
		for {
			select {
			case <-bctx.Done():
				break wait
			case <-deadline.C:
				reason = "timeout"
				break wait
			case now := <-tick.C:
				if done, why := w.step(now, m.behaviorObs()); done {
					reason = why
					break wait
				}
			}
		}
		tick.Stop()
		deadline.Stop()
	}
	if reason == "" { // cancelled: stop, the program's end, the caller went away
		m.progMu.Lock()
		if m.prog == p {
			p.behavior = ""
		}
		m.progMu.Unlock()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("stopped")
	}
	control, err := resume()
	if err != nil {
		return nil, err
	}
	return map[string]any{"behavior": name, "finished": reason == "finished", "reason": reason,
		"seconds": round(time.Since(started).Seconds(), 0.1), "control": control}, nil
}

// behaviorEndingProgram: the program ends (reason "charger"), then Vector goes home on its own.
func (m *Manager) behaviorEndingProgram(ctx context.Context, cli vectorpb.ExternalInterfaceClient, name string, b Behavior) (any, error) {
	m.progMu.Lock()
	p := m.prog
	m.progMu.Unlock()
	if p == nil || p.starting {
		return nil, ErrSessionRequired
	}
	m.endProgram(p.id, "charger", "")
	time.Sleep(behaviorLetGo)
	if _, err := cli.AppIntent(ctx, &vectorpb.AppIntentRequest{Intent: b.Intent}); err != nil {
		return nil, fmt.Errorf("robot: %w", err)
	}
	return map[string]any{"behavior": name, "program_ended": true, "reason": "charger"}, nil
}

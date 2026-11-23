package robot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

// Program sessions: a step-by-step program (Robots Code Studio) drives Vector through the usual
// commands while the bridge keeps behavior control for the whole run, keeps everybody else's
// motion commands out, and stops the robot when the program stops talking to it:
//
//   - program_begin takes and holds control (no 2-second release, no 600-second hold limit);
//   - no heartbeat for `heartbeat` seconds → watchdog: motors stop, control is released;
//   - the session ends at its time limit, when Vector takes control back (a cliff, being picked
//     up), when the robot or the program's connection goes away, and on any `stop` — a `stop` from
//     Home Assistant always ends a child's program (reason "ha_stop").
//
// The session id is a capability: commands that carry it (or come from the connection that began
// the session) pass the lock. It is never broadcast.

var (
	ErrProgramRunning  = errors.New("program running")
	ErrNoSession       = errors.New("no such program session")
	ErrSessionRequired = errors.New("program session required")
	ErrBatteryLow      = errors.New("battery low")
)

const (
	programLimitDefault     = 300.0
	programLimitMax         = 1800.0
	programHeartbeatDefault = 3.0
	programHeartbeatMax     = 30.0
	programHoldGrace        = 30 * time.Second // control is held a bit past the limit; the limit timer ends it
	lowBatteryPercent       = 10
)

// lowest limit and heartbeat in seconds (variables: tests use shorter ones)
var (
	programLimitMin     = 10.0
	programHeartbeatMin = 1.0
)

// ProgramInfo is the running program as everybody sees it (state `program`, hello `program`).
type ProgramInfo struct {
	Owner string `json:"owner"`
	Since int64  `json:"since"` // unix seconds
	Limit int    `json:"limit"` // seconds
}

type program struct {
	id, owner string
	caller    string // connection that began it
	since     time.Time
	limit     time.Duration
	heartbeat time.Duration
	starting  bool
	watch     *time.Timer
	deadline  *time.Timer
}

// Caller says who sends a command: the WebSocket connection, and whether it logged in with the
// studio token (then motion commands need its own program session).
type Caller struct {
	ID     string
	Studio bool
}

type callerKey struct{}

// WithCaller tags a command's context with its sender.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerOf: the sender of a command (zero Caller = internal / unknown).
func CallerOf(ctx context.Context) Caller {
	c, _ := ctx.Value(callerKey{}).(Caller)
	return c
}

// lockedCommands move, animate or make Vector speak: during a program only its owner may send them.
var lockedCommands = map[string]bool{
	"move": true, "say_text": true, "hold_control": true, "release_control": true,
	"app_intent": true, "enroll_face": true, "set_eye_color": true,
	"connect_cube": true, "disconnect_cube": true, "flash_cube": true, "forget_cube": true,
	"set_wheels": true, "set_cube_lights": true, "play_audio": true,
}

func isLocked(name string) bool {
	if lockedCommands[name] {
		return true
	}
	_, ok := actions[name]
	return ok
}

// studioFree: commands the studio token may send without a program session of its own.
var studioFree = map[string]bool{
	"program_begin": true, "program_heartbeat": true, "program_end": true, "sensors": true,
	"refresh": true, "list_animations": true, "list_animation_triggers": true, "list_faces": true,
	"stop": true, "stop_audio": true,
}

func newSessionID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ownsLocked: the command comes from the session's owner (mu held).
func (p *program) owns(c Caller, session string) bool {
	return p != nil && (session != "" && session == p.id || c.ID != "" && c.ID == p.caller)
}

// guard applies the program lock and renews the watchdog of the owner's session.
func (m *Manager) guard(c Caller, name, session string) error {
	m.progMu.Lock()
	p := m.prog
	owner := p.owns(c, session)
	if owner && !p.starting {
		p.watch.Reset(p.heartbeat)
	}
	m.progMu.Unlock()
	if c.Studio && !studioFree[name] && !owner {
		if p != nil && !p.starting && session == "" {
			return ErrProgramRunning
		}
		return ErrSessionRequired
	}
	if p != nil && !owner && isLocked(name) {
		return ErrProgramRunning
	}
	return nil
}

// GuardAudio checks POST /audio (a sound of the program, or Home Assistant speaking through Vector).
func (m *Manager) GuardAudio(c Caller, session string) error {
	return m.guard(c, "play_audio", session)
}

// Program is the running session (nil when none) as everybody sees it.
func (m *Manager) Program() *ProgramInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.tr.S.Program; p != nil {
		c := *p
		return &c
	}
	return nil
}

// SensorsNow reads the sensors at once.
func (m *Manager) SensorsNow() Sensors {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tr.Sensors(time.Now())
}

// NoteSpeech records what wire-pod heard after "Hey Vector" (for `sensors`).
func (m *Manager) NoteSpeech(text string) {
	m.mu.Lock()
	m.tr.NoteSpeech(text, time.Now())
	m.mu.Unlock()
}

// CallerGone ends the program of a connection that closed.
func (m *Manager) CallerGone(id string) {
	m.progMu.Lock()
	p := m.prog
	mine := p != nil && id != "" && p.caller == id
	m.progMu.Unlock()
	if mine {
		m.endProgram(p.id, "disconnected", "client")
	}
}

// programCommand runs program_begin / _heartbeat / _end, sensors, set_wheels, set_cube_lights.
func (m *Manager) programCommand(ctx context.Context, name string, a Args) (any, error) {
	session := a.str("session", "")
	switch name {
	case "sensors":
		return m.SensorsNow(), nil
	case "program_begin":
		return m.programBegin(ctx, a)
	case "program_heartbeat":
		m.progMu.Lock()
		p := m.prog
		ok := p != nil && !p.starting && session == p.id
		var remaining float64
		if ok {
			remaining = round(time.Until(p.since.Add(p.limit)).Seconds(), 0.1)
		}
		m.progMu.Unlock()
		if !ok {
			return nil, ErrNoSession
		}
		return map[string]any{"remaining": remaining, "sensors": m.SensorsNow()}, nil
	case "program_end":
		m.progMu.Lock()
		p := m.prog
		ok := p != nil && session != "" && session == p.id
		m.progMu.Unlock()
		if !ok {
			return nil, ErrNoSession
		}
		m.endProgram(session, endReason(a.str("reason", "")), "")
		return nil, nil
	case "set_wheels":
		m.progMu.Lock()
		owner := m.prog.owns(CallerOf(ctx), session) && !m.prog.starting
		m.progMu.Unlock()
		if !owner {
			return nil, ErrSessionRequired
		}
		cli, err := m.client()
		if err != nil {
			return nil, err
		}
		left, right := float32(clamp(a.num("left", 0), -220, 220)), float32(clamp(a.num("right", 0), -220, 220))
		_, err = cli.DriveWheels(ctx, &vectorpb.DriveWheelsRequest{LeftWheelMmps: left, RightWheelMmps: right, LeftWheelMmps2: 400, RightWheelMmps2: 400})
		return nil, err
	case "set_cube_lights":
		cli, err := m.client()
		if err != nil {
			return nil, err
		}
		return nil, m.setCubeLights(ctx, cli, a)
	}
	return nil, fmt.Errorf("unknown command %q", name)
}

var reasonRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// endReason: the program's own reason (end, stop, error…), "end" when missing or odd.
func endReason(r string) string {
	r = strings.ToLower(strings.TrimSpace(r))
	if !reasonRe.MatchString(r) {
		return "end"
	}
	return r
}

func (m *Manager) programBegin(ctx context.Context, a Args) (any, error) {
	cli, err := m.client()
	if err != nil {
		return nil, err
	}
	owner := strings.TrimSpace(a.str("owner", ""))
	if owner == "" {
		owner = "program"
	}
	if r := []rune(owner); len(r) > 40 {
		owner = string(r[:40])
	}
	limit := time.Duration(clamp(a.num("limit", programLimitDefault), programLimitMin, programLimitMax) * float64(time.Second))
	heartbeat := time.Duration(clamp(a.num("heartbeat", programHeartbeatDefault), programHeartbeatMin, programHeartbeatMax) * float64(time.Second))

	m.progMu.Lock()
	if m.prog != nil {
		m.progMu.Unlock()
		return nil, ErrProgramRunning
	}
	if b := m.Snapshot().Battery; b != nil && (b.Level == "low" || b.Percent < lowBatteryPercent) {
		m.progMu.Unlock()
		return nil, ErrBatteryLow
	}
	p := &program{id: newSessionID(), owner: owner, caller: CallerOf(ctx).ID, limit: limit, heartbeat: heartbeat, starting: true}
	m.prog = p
	m.progMu.Unlock()

	// stopped (stop, lost robot) while starting: m.prog is no longer p
	abort := func(err error) (any, error) {
		m.progMu.Lock()
		mine, other := m.prog == p, m.prog != nil && m.prog != p
		if mine {
			m.prog = nil
		}
		m.progMu.Unlock()
		if !other {
			m.lease.Release()
		}
		return nil, err
	}
	if err := m.lease.Hold(ctx, limit+programHoldGrace); err != nil {
		return abort(err)
	}
	// somebody was driving from the dashboard: that stops now (its dead man's switch would
	// otherwise zero the program's first wheel speeds)
	m.mover.mu.Lock()
	if m.mover.done != nil {
		m.stopMotorsLocked(cli)
	}
	m.mover.mu.Unlock()
	if a.boolean("drive_off_charger", false) {
		if b := m.Snapshot().Battery; b != nil && b.OnCharger {
			if _, err := m.runAction(ctx, cli, actions["drive_off_charger"], nil); err != nil {
				return abort(fmt.Errorf("drive off charger: %w", err))
			}
		}
	}

	now := time.Now()
	m.progMu.Lock()
	if m.prog != p {
		m.progMu.Unlock()
		return abort(errors.New("program stopped"))
	}
	p.starting, p.since = false, now
	id := p.id
	p.watch = time.AfterFunc(heartbeat, func() { m.endProgram(id, "watchdog", "") })
	p.deadline = time.AfterFunc(limit, func() { m.endProgram(id, "limit", "") })
	m.progMu.Unlock()

	info := &ProgramInfo{Owner: owner, Since: now.Unix(), Limit: int(limit / time.Second)}
	m.mu.Lock()
	m.tr.S.Program = info
	m.tr.ResetCounts()
	m.dirty = true
	sensors := m.tr.Sensors(now)
	st := m.snapshotLocked()
	esn := m.tr.S.ESN
	m.mu.Unlock()
	m.log.Info("program started", "owner", owner, "limit", limit, "heartbeat", heartbeat)
	m.sink.Event(esn, Event{Name: "program_started", Data: map[string]any{
		"owner": owner, "since": info.Since, "limit": info.Limit, "heartbeat": heartbeat.Seconds()}})
	return map[string]any{"session": id, "limit": info.Limit, "heartbeat": heartbeat.Seconds(),
		"state": st, "sensors": sensors}, nil
}

// endProgram ends session id ("" = whichever runs): motors stop, the running action is cancelled,
// the bridge's audio stops, control is released and program_ended goes to everybody.
func (m *Manager) endProgram(id, reason, cause string) bool {
	m.progMu.Lock()
	p := m.prog
	if p == nil || id != "" && p.id != id {
		m.progMu.Unlock()
		return false
	}
	m.prog = nil
	if p.watch != nil {
		p.watch.Stop()
	}
	if p.deadline != nil {
		p.deadline.Stop()
	}
	m.progMu.Unlock()

	m.haltMotion()
	m.mu.Lock()
	m.tr.S.Program = nil
	m.dirty = true
	esn := m.tr.S.ESN
	m.mu.Unlock()
	if p.starting {
		return true // program_started was never sent; program_begin reports the failure
	}
	data := map[string]any{"owner": p.owner, "reason": reason, "duration": round(time.Since(p.since).Seconds(), 0.1)}
	if cause != "" {
		data["cause"] = cause
	}
	m.log.Info("program ended", "owner", p.owner, "reason", reason, "cause", cause)
	m.sink.Event(esn, Event{Name: "program_ended", Data: data})
	return true
}

// haltMotion: everything a stop does — motors, the running action, audio, control.
func (m *Manager) haltMotion() {
	m.cancelAction()
	m.Player.Stop()
	if cli, err := m.client(); err == nil {
		m.mover.mu.Lock()
		if m.mover.done != nil {
			m.stopMotorsLocked(cli)
		}
		m.mover.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, _ = cli.DriveWheels(ctx, &vectorpb.DriveWheelsRequest{})
		_, _ = cli.StopAllMotors(ctx, &vectorpb.StopAllMotorsRequest{})
		cancel()
	}
	m.lease.Release()
}

// stopProgram: `stop` always ends a program — "stop" from its owner, "ha_stop" from anybody else.
func (m *Manager) stopProgram(c Caller, session string) {
	m.progMu.Lock()
	p := m.prog
	owner := p.owns(c, session)
	m.progMu.Unlock()
	if p == nil {
		return
	}
	reason := "ha_stop"
	if owner {
		reason = "stop"
	}
	m.endProgram(p.id, reason, "")
}

// controlDropped: Vector took control back (cliff, picked up…) or the control stream broke.
func (m *Manager) controlDropped(lost bool) {
	m.progMu.Lock()
	p := m.prog
	running := p != nil && !p.starting
	m.progMu.Unlock()
	if !running {
		return
	}
	cause := "connection"
	if lost {
		m.mu.Lock()
		cause = m.tr.lossCause(time.Now())
		m.mu.Unlock()
	}
	m.endProgram(p.id, "control_lost", cause)
}

// ---- cube lights ----

// setCubeLights: color "#rrggbb" or hue + saturation (0–1), mode on | flash | off.
func (m *Manager) setCubeLights(ctx context.Context, cli vectorpb.ExternalInterfaceClient, a Args) error {
	id, err := m.cubeID()
	if err != nil {
		return err
	}
	mode := a.str("mode", "on")
	var color uint32
	if mode != "off" {
		color, err = cubeColor(a)
		if err != nil {
			return err
		}
	}
	on, off, onMs, offMs := color, color, uint32(250), uint32(0)
	switch mode {
	case "on":
	case "flash":
		off, onMs, offMs = 0, 300, 300
	case "off":
		on, off = 0, 0
	default:
		return errors.New("mode: one of on, flash, off")
	}
	four := func(v uint32) []uint32 { return []uint32{v, v, v, v} }
	_, err = cli.SetCubeLights(ctx, &vectorpb.SetCubeLightsRequest{
		ObjectId: uint32(id), OnColor: four(on), OffColor: four(off), OnPeriodMs: four(onMs), OffPeriodMs: four(offMs),
		TransitionOnPeriodMs: four(0), TransitionOffPeriodMs: four(0), Offset: []int32{0, 0, 0, 0},
		MakeRelative: vectorpb.SetCubeLightsRequest_OFF,
	})
	return err
}

var hexColorRe = regexp.MustCompile(`^#?([0-9a-fA-F]{6})$`)

// cubeColor → 0xRRGGBBAA as the cube wants it.
func cubeColor(a Args) (uint32, error) {
	if a.has("color") {
		var s string
		_ = json.Unmarshal(a["color"], &s)
		mt := hexColorRe.FindStringSubmatch(strings.TrimSpace(s))
		if mt == nil {
			return 0, errors.New(`color: "#rrggbb"`)
		}
		var rgb uint32
		_, _ = fmt.Sscanf(mt[1], "%06x", &rgb)
		return rgb<<8 | 0xff, nil
	}
	if !a.has("hue") {
		return 0, errors.New("color or hue required")
	}
	r, g, b := hsvToRGB(clamp(a.num("hue", 0), 0, 1), clamp(a.num("saturation", 1), 0, 1), 1)
	return uint32(r)<<24 | uint32(g)<<16 | uint32(b)<<8 | 0xff, nil
}

func hsvToRGB(h, s, v float64) (uint8, uint8, uint8) {
	h = h * 6
	if h >= 6 {
		h = 0
	}
	i := int(h)
	f := h - float64(i)
	p, q, t := v*(1-s), v*(1-s*f), v*(1-s*(1-f))
	var r, g, b float64
	switch i {
	case 0:
		r, g, b = v, t, p
	case 1:
		r, g, b = q, v, p
	case 2:
		r, g, b = p, v, t
	case 3:
		r, g, b = p, q, v
	case 4:
		r, g, b = t, p, v
	default:
		r, g, b = v, p, q
	}
	to8 := func(x float64) uint8 { return uint8(x*255 + 0.5) }
	return to8(r), to8(g), to8(b)
}

package robot

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

func (f *fakeRobot) DriveOffCharger(context.Context, *vectorpb.DriveOffChargerRequest) (*vectorpb.DriveOffChargerResponse, error) {
	f.mu.Lock()
	f.offCharger++
	f.mu.Unlock()
	return &vectorpb.DriveOffChargerResponse{Result: vectorpb.BehaviorResults_BEHAVIOR_COMPLETE_STATE}, nil
}

func (f *fakeRobot) SetCubeLights(_ context.Context, r *vectorpb.SetCubeLightsRequest) (*vectorpb.SetCubeLightsResponse, error) {
	f.mu.Lock()
	f.cubeLights = append(f.cubeLights, r)
	f.mu.Unlock()
	return &vectorpb.SetCubeLightsResponse{}, nil
}

func jsonArgs(t *testing.T, kv map[string]any) Args {
	t.Helper()
	a := Args{}
	for k, v := range kv {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		a[k] = b
	}
	return a
}

var (
	studio = Caller{ID: "ws-studio", Studio: true}
	ha     = Caller{ID: "ws-ha"}
)

// begin starts a session from the studio and returns its id.
func begin(t *testing.T, m *Manager, kv map[string]any) string {
	t.Helper()
	if kv == nil {
		kv = map[string]any{}
	}
	if _, ok := kv["owner"]; !ok {
		kv["owner"] = "rcs"
	}
	res, err := m.Command(WithCaller(context.Background(), studio), "program_begin", jsonArgs(t, kv))
	if err != nil {
		t.Fatal(err)
	}
	r := res.(map[string]any)
	id, _ := r["session"].(string)
	if len(id) != 24 {
		t.Fatalf("session id %q", id)
	}
	if _, ok := r["sensors"].(Sensors); !ok {
		t.Fatalf("begin without sensors: %v", r)
	}
	if _, ok := r["state"].(State); !ok {
		t.Fatalf("begin without state: %v", r)
	}
	return id
}

func shortTimes(t *testing.T) {
	t.Helper()
	lim, hb := programLimitMin, programHeartbeatMin
	programLimitMin, programHeartbeatMin = 0.3, 0.2
	t.Cleanup(func() { programLimitMin, programHeartbeatMin = lim, hb })
}

func TestProgramHoldsControlUntilEnd(t *testing.T) {
	robot, sink, m := start(t)
	id := begin(t, m, map[string]any{"heartbeat": 5})
	if p := m.Snapshot().Program; p == nil || p.Owner != "rcs" || p.Limit != 300 {
		t.Fatalf("state program %+v", p)
	}
	if e := sink.last("program_started"); e == nil || e.Data["owner"] != "rcs" || e.Data["session"] != nil {
		t.Fatalf("program_started %+v (must not carry the session id)", e)
	}
	// an action, then much longer than ReleaseAfter: control stays
	if _, err := m.Command(WithCaller(context.Background(), studio), "set_head_angle", Args{"angle": []byte("10")}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	robot.get(func() {
		if robot.controls != 1 || robot.releases != 0 {
			t.Fatalf("controls=%d releases=%d (control must be held)", robot.controls, robot.releases)
		}
	})
	if m.Snapshot().Control != "held" {
		t.Fatalf("control %q", m.Snapshot().Control)
	}
	r, err := m.Command(context.Background(), "program_heartbeat", Args{"session": []byte(`"` + id + `"`)})
	if err != nil {
		t.Fatal(err)
	}
	if rem := r.(map[string]any)["remaining"].(float64); rem < 290 || rem > 300 {
		t.Fatalf("remaining %v", rem)
	}
	if _, err := m.Command(context.Background(), "program_heartbeat", Args{"session": []byte(`"nope"`)}); !errors.Is(err, ErrNoSession) {
		t.Fatalf("wrong session heartbeat: %v", err)
	}
	if _, err := m.Command(context.Background(), "program_end", jsonArgs(t, map[string]any{"session": id, "reason": "end"})); err != nil {
		t.Fatal(err)
	}
	e := sink.last("program_ended")
	if e == nil || e.Data["reason"] != "end" || e.Data["owner"] != "rcs" {
		t.Fatalf("program_ended %+v", e)
	}
	if m.Snapshot().Program != nil {
		t.Fatal("program still in state")
	}
	eventually(t, "release", func() (ok bool) {
		robot.get(func() { ok = robot.releases == 1 && robot.stops >= 1 })
		return ok
	})
	if _, err := m.Command(context.Background(), "program_end", jsonArgs(t, map[string]any{"session": id})); !errors.Is(err, ErrNoSession) {
		t.Fatalf("second end: %v", err)
	}
}

func TestProgramLocksOthersOut(t *testing.T) {
	robot, sink, m := start(t)
	id := begin(t, m, map[string]any{"heartbeat": 5})
	haCtx := WithCaller(context.Background(), ha)
	if _, err := m.Command(WithCaller(context.Background(), Caller{ID: "ws-other", Studio: true}), "program_begin", Args{}); !errors.Is(err, ErrProgramRunning) {
		t.Fatalf("second program: %v", err)
	}
	for _, cmd := range []string{"move", "set_head_angle", "say_text", "hold_control", "release_control", "play_animation_trigger", "set_eye_color", "drive_wheels", "app_intent"} {
		if _, err := m.Command(haCtx, cmd, Args{"left": []byte("50"), "text": []byte(`"hi"`)}); !errors.Is(err, ErrProgramRunning) {
			t.Fatalf("%s from HA during a program: %v", cmd, err)
		}
	}
	if err := m.GuardAudio(Caller{}, ""); !errors.Is(err, ErrProgramRunning) {
		t.Fatalf("HA audio during a program: %v", err)
	}
	if err := m.GuardAudio(Caller{Studio: true}, id); err != nil {
		t.Fatalf("program audio: %v", err)
	}
	// reading is fine, and the session id opens the lock from any connection
	if _, err := m.Command(haCtx, "refresh", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Command(haCtx, "sensors", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Command(haCtx, "set_head_angle", jsonArgs(t, map[string]any{"angle": 5, "session": id})); err != nil {
		t.Fatalf("with the session id: %v", err)
	}
	// stop from Home Assistant always ends the child's program
	if _, err := m.Command(haCtx, "stop", nil); err != nil {
		t.Fatal(err)
	}
	if e := sink.last("program_ended"); e == nil || e.Data["reason"] != "ha_stop" {
		t.Fatalf("program_ended %+v", e)
	}
	if _, err := m.Command(haCtx, "set_head_angle", Args{}); err != nil {
		t.Fatalf("after the program: %v", err)
	}
	robot.get(func() {
		if robot.stops < 2 {
			t.Fatalf("stops=%d", robot.stops)
		}
	})
}

func TestProgramStopFromOwner(t *testing.T) {
	_, sink, m := start(t)
	begin(t, m, nil)
	if _, err := m.Command(WithCaller(context.Background(), studio), "stop", nil); err != nil {
		t.Fatal(err)
	}
	if e := sink.last("program_ended"); e == nil || e.Data["reason"] != "stop" {
		t.Fatalf("program_ended %+v", e)
	}
}

func TestStudioNeedsItsSession(t *testing.T) {
	_, _, m := start(t)
	sctx := WithCaller(context.Background(), studio)
	for _, cmd := range []string{"drive_straight", "say_text", "set_wheels", "set_cube_lights"} {
		if _, err := m.Command(sctx, cmd, Args{}); !errors.Is(err, ErrSessionRequired) {
			t.Fatalf("%s from the studio without a session: %v", cmd, err)
		}
	}
	if err := m.GuardAudio(Caller{Studio: true}, ""); !errors.Is(err, ErrSessionRequired) {
		t.Fatalf("studio audio without a session: %v", err)
	}
	for _, cmd := range []string{"sensors", "refresh"} {
		if _, err := m.Command(sctx, cmd, nil); err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
	}
	// Home Assistant without a program: set_wheels is for programs only
	if _, err := m.Command(WithCaller(context.Background(), ha), "set_wheels", Args{}); !errors.Is(err, ErrSessionRequired) {
		t.Fatalf("set_wheels without a program: %v", err)
	}
}

func TestProgramWatchdogStopsWheels(t *testing.T) {
	shortTimes(t)
	robot, sink, m := start(t)
	id := begin(t, m, map[string]any{"heartbeat": 0.3})
	if _, err := m.Command(context.Background(), "set_wheels", jsonArgs(t, map[string]any{"session": id, "left": 300, "right": -50})); err != nil {
		t.Fatal(err)
	}
	robot.get(func() {
		if w := robot.wheelsBoth[len(robot.wheelsBoth)-1]; w != [2]float32{220, -50} {
			t.Fatalf("wheels %v (clamped to ±220)", w)
		}
	})
	// heartbeats keep it going
	for range 4 {
		time.Sleep(150 * time.Millisecond)
		if _, err := m.Command(context.Background(), "program_heartbeat", jsonArgs(t, map[string]any{"session": id})); err != nil {
			t.Fatal(err)
		}
	}
	if sink.has("program_ended") {
		t.Fatal("ended despite heartbeats")
	}
	eventually(t, "watchdog", func() bool { return sink.has("program_ended") })
	if e := sink.last("program_ended"); e.Data["reason"] != "watchdog" {
		t.Fatalf("program_ended %+v", e)
	}
	robot.get(func() {
		if w := robot.wheelsBoth[len(robot.wheelsBoth)-1]; w != [2]float32{0, 0} || robot.stops < 1 {
			t.Fatalf("wheels %v stops %d after the watchdog", w, robot.stops)
		}
	})
	eventually(t, "released", func() (ok bool) {
		robot.get(func() { ok = robot.releases == 1 })
		return ok
	})
}

func TestProgramTimeLimit(t *testing.T) {
	shortTimes(t)
	_, sink, m := start(t)
	id := begin(t, m, map[string]any{"limit": 0.3, "heartbeat": 5})
	_ = id
	eventually(t, "limit", func() bool { return sink.has("program_ended") })
	if e := sink.last("program_ended"); e.Data["reason"] != "limit" {
		t.Fatalf("program_ended %+v", e)
	}
}

func TestProgramControlLostByCliff(t *testing.T) {
	robot, sink, m := start(t)
	begin(t, m, map[string]any{"heartbeat": 5})
	robot.events <- &vectorpb.Event{EventType: &vectorpb.Event_RobotState{RobotState: &vectorpb.RobotState{Status: uint32(vectorpb.RobotStatus_ROBOT_STATUS_CLIFF_DETECTED)}}}
	eventually(t, "cliff", func() bool { return sink.has("cliff") })
	robot.loseCtl <- struct{}{}
	eventually(t, "program_ended", func() bool { return sink.has("program_ended") })
	e := sink.last("program_ended")
	if e.Data["reason"] != "control_lost" || e.Data["cause"] != "cliff" {
		t.Fatalf("program_ended %+v", e)
	}
	if !sink.has("control_lost") {
		t.Fatal("no control_lost event")
	}
	if m.Snapshot().Program != nil {
		t.Fatal("program still running")
	}
}

func TestProgramEndsWhenCallerOrRobotGoes(t *testing.T) {
	robot, sink, m := start(t)
	begin(t, m, map[string]any{"heartbeat": 5})
	m.CallerGone("ws-someone-else")
	if sink.has("program_ended") {
		t.Fatal("ended for another connection")
	}
	m.CallerGone(studio.ID)
	if e := sink.last("program_ended"); e == nil || e.Data["reason"] != "disconnected" || e.Data["cause"] != "client" {
		t.Fatalf("program_ended %+v", e)
	}

	begin(t, m, map[string]any{"heartbeat": 5})
	robot.endStream <- struct{}{}
	eventually(t, "ended by the robot", func() bool {
		e := sink.last("program_ended")
		return e != nil && e.Data["cause"] == "robot"
	})
}

func TestProgramBatteryLowAndDisconnected(t *testing.T) {
	_, _, m := start(t)
	m.update(func(t *Tracker) bool { t.S.Battery.Level = "low"; return true })
	if _, err := m.Command(context.Background(), "program_begin", Args{}); !errors.Is(err, ErrBatteryLow) {
		t.Fatalf("low battery: %v", err)
	}
	m2 := NewManager(Config{IP: "x", GUID: "y"}, m.log, &fakeSink{}, nil)
	if _, err := m2.Command(context.Background(), "program_begin", Args{}); err == nil || err.Error() != "robot not connected" {
		t.Fatalf("disconnected: %v", err)
	}
}

func TestProgramDrivesOffCharger(t *testing.T) {
	robot, _, m := start(t)
	begin(t, m, map[string]any{"drive_off_charger": true, "heartbeat": 5})
	robot.get(func() {
		if robot.offCharger != 1 {
			t.Fatalf("drive_off_charger calls %d", robot.offCharger)
		}
	})
}

func TestSensorsAndCounters(t *testing.T) {
	robot, sink, m := start(t)
	m.NoteSpeech("old sentence") // before the session: not counted in it
	id := begin(t, m, map[string]any{"heartbeat": 5})
	status := uint32(vectorpb.RobotStatus_ROBOT_STATUS_IS_BUTTON_PRESSED | vectorpb.RobotStatus_ROBOT_STATUS_IS_FALLING)
	robot.events <- &vectorpb.Event{EventType: &vectorpb.Event_RobotState{RobotState: &vectorpb.RobotState{
		Status:             status,
		ProxData:           &vectorpb.ProxData{DistanceMm: 123, SignalQuality: 0.8, FoundObject: true},
		Accel:              &vectorpb.AccelData{X: 1, Y: 2, Z: 9800},
		TouchData:          &vectorpb.TouchData{IsBeingTouched: true},
		LeftWheelSpeedMmps: 50, RightWheelSpeedMmps: 49.94,
	}}}
	robot.events <- &vectorpb.Event{EventType: &vectorpb.Event_RobotObservedFace{RobotObservedFace: &vectorpb.RobotObservedFace{FaceId: 7, Name: "Alice"}}}
	robot.events <- &vectorpb.Event{EventType: &vectorpb.Event_ObjectEvent{ObjectEvent: &vectorpb.ObjectEvent{ObjectEventType: &vectorpb.ObjectEvent_RobotObservedObject{
		RobotObservedObject: &vectorpb.RobotObservedObject{ObjectType: vectorpb.ObjectType_BLOCK_LIGHTCUBE1}}}}}
	robot.events <- &vectorpb.Event{EventType: &vectorpb.Event_ObjectEvent{ObjectEvent: &vectorpb.ObjectEvent{ObjectEventType: &vectorpb.ObjectEvent_ObjectTapped{
		ObjectTapped: &vectorpb.ObjectTapped{}}}}}
	robot.events <- &vectorpb.Event{EventType: &vectorpb.Event_WakeWord{WakeWord: &vectorpb.WakeWord{WakeWordType: &vectorpb.WakeWord_WakeWordBegin{WakeWordBegin: &vectorpb.WakeWordBegin{}}}}}
	eventually(t, "events", func() bool { return sink.has("cube_tapped") && sink.has("wake_word") })
	m.NoteSpeech("dance for me")

	res, err := m.Command(context.Background(), "sensors", jsonArgs(t, map[string]any{"session": id}))
	if err != nil {
		t.Fatal(err)
	}
	s := res.(Sensors)
	if !s.Touched || !s.Falling || !s.ObstacleOK || s.ObstacleMM != 123 || s.ObstacleSignal != 0.8 || s.LiftInView {
		t.Fatalf("sensors %+v", s)
	}
	if s.Accel.Z != 9800 || s.Wheels.Left != 50 || s.Wheels.Right != 50 || !s.Connected || s.OnCharger || s.At == 0 {
		t.Fatalf("sensors %+v", s)
	}
	want := Counters{Touches: 1, ButtonPresses: 1, WakeWords: 1, Speech: 1, CubeTaps: 1}
	if s.Counts != want {
		t.Fatalf("counts %+v, want %+v", s.Counts, want)
	}
	if s.Face == nil || s.Face.Name != "Alice" || s.Face.ID != 7 || s.Face.AgeS > 1 {
		t.Fatalf("face %+v", s.Face)
	}
	if !s.Cube.Visible || s.Cube.Taps != 1 || s.Cube.SeenAgeS == nil {
		t.Fatalf("cube %+v", s.Cube)
	}
	if s.LastSpeech == nil || s.LastSpeech.Text != "dance for me" {
		t.Fatalf("speech %+v", s.LastSpeech)
	}
	// the lift in front of the sensor: no valid distance
	robot.events <- &vectorpb.Event{EventType: &vectorpb.Event_RobotState{RobotState: &vectorpb.RobotState{
		ProxData: &vectorpb.ProxData{DistanceMm: 30, FoundObject: true, IsLiftInFov: true}}}}
	eventually(t, "lift in view", func() bool { return m.SensorsNow().LiftInView })
	if m.SensorsNow().ObstacleOK {
		t.Fatal("obstacle_ok with the lift in view")
	}
}

func TestSetCubeLights(t *testing.T) {
	robot, _, m := start(t)
	if _, err := m.Command(context.Background(), "set_cube_lights", jsonArgs(t, map[string]any{"color": "#ff0000"})); err == nil {
		t.Fatal("cube lights without a cube")
	}
	m.update(func(t *Tracker) bool { c := t.cube(); c.Connected, c.ObjectID = true, 5; return true })
	for _, tc := range []struct {
		args         map[string]any
		on, off, per uint32
	}{
		{map[string]any{"color": "#ff8000"}, 0xff8000ff, 0xff8000ff, 0},
		{map[string]any{"hue": 1.0 / 3, "saturation": 1, "mode": "flash"}, 0x00ff00ff, 0, 300},
		{map[string]any{"mode": "off"}, 0, 0, 0},
	} {
		if _, err := m.Command(context.Background(), "set_cube_lights", jsonArgs(t, tc.args)); err != nil {
			t.Fatal(err)
		}
		robot.get(func() {
			r := robot.cubeLights[len(robot.cubeLights)-1]
			if r.GetObjectId() != 5 || len(r.GetOnColor()) != 4 || r.GetOnColor()[0] != tc.on || r.GetOffColor()[0] != tc.off || r.GetOffPeriodMs()[0] != tc.per {
				t.Fatalf("%v → %v", tc.args, r)
			}
		})
	}
	for _, bad := range []map[string]any{{"color": "red"}, {"mode": "blink", "color": "#000000"}, {}} {
		if _, err := m.Command(context.Background(), "set_cube_lights", jsonArgs(t, bad)); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
}

func TestEndReason(t *testing.T) {
	for in, want := range map[string]string{"": "end", "stop": "stop", "Error": "error", "x y": "end", "a_very_long_reason_that_is_way_over_limit": "end"} {
		if got := endReason(in); got != want {
			t.Fatalf("endReason(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProgramStopsDashboardDriving(t *testing.T) {
	robot, _, m := start(t)
	if _, err := m.Command(WithCaller(context.Background(), ha), "move", jsonArgs(t, map[string]any{"left": 100, "right": 100})); err != nil {
		t.Fatal(err)
	}
	begin(t, m, map[string]any{"heartbeat": 5})
	robot.get(func() {
		if w := robot.wheelsBoth[len(robot.wheelsBoth)-1]; w != [2]float32{0, 0} {
			t.Fatalf("wheels %v: the card's driving must stop when a program begins", w)
		}
	})
	m.mover.mu.Lock()
	defer m.mover.mu.Unlock()
	if m.mover.done != nil || m.mover.timer != nil {
		t.Fatal("the card's dead man's switch is still armed")
	}
}

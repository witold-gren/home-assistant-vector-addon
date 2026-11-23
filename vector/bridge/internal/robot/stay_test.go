package robot

import (
	"context"
	"errors"
	"testing"
	"time"
)

func stayCmd(t *testing.T, m *Manager, c Caller, on any) map[string]any {
	t.Helper()
	args := Args{}
	if on != nil {
		args = jsonArgs(t, map[string]any{"on": on})
	}
	res, err := m.Command(WithCaller(context.Background(), c), "stay_still", args)
	if err != nil {
		t.Fatal(err)
	}
	return res.(map[string]any)
}

func shortStay(t *testing.T) {
	t.Helper()
	r, d := stayRetry, stayTimeoutDefault
	stayRetry = 50 * time.Millisecond
	t.Cleanup(func() { stayRetry, stayTimeoutDefault = r, d })
}

func TestStayStillHoldsAndLetsGo(t *testing.T) {
	robot, _, m := start(t)
	if r := stayCmd(t, m, ha, nil); r["stay_still"] != false {
		t.Fatalf("query %v", r)
	}
	if r := stayCmd(t, m, ha, true); r["stay_still"] != true {
		t.Fatalf("on %v", r)
	}
	if !m.Snapshot().StayStill || m.Snapshot().Control != "held" {
		t.Fatalf("state stay=%v control=%q", m.Snapshot().StayStill, m.Snapshot().Control)
	}
	// much longer than ReleaseAfter, and a dashboard command in between: control stays
	if _, err := m.Command(WithCaller(context.Background(), ha), "set_head_angle", Args{"angle": []byte("5")}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	robot.get(func() {
		if robot.controls != 1 || robot.releases != 0 {
			t.Fatalf("controls=%d releases=%d", robot.controls, robot.releases)
		}
	})
	// stop only stops: Vector keeps standing still
	if _, err := m.Command(WithCaller(context.Background(), ha), "stop", nil); err != nil {
		t.Fatal(err)
	}
	robot.get(func() {
		if robot.releases != 0 {
			t.Fatalf("stop released control (%d)", robot.releases)
		}
	})
	// a short manual hold never cuts standing still
	if _, err := m.Command(WithCaller(context.Background(), ha), "hold_control", Args{"seconds": []byte("1")}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1200 * time.Millisecond)
	robot.get(func() {
		if robot.releases != 0 {
			t.Fatalf("hold_control 1 s released standing still")
		}
	})
	if r := stayCmd(t, m, ha, false); r["stay_still"] != false {
		t.Fatalf("off %v", r)
	}
	eventually(t, "release", func() (ok bool) {
		robot.get(func() { ok = robot.releases == 1 })
		return ok
	})
	if m.Snapshot().StayStill {
		t.Fatal("state still on")
	}
}

func TestStayStillFromStudioToken(t *testing.T) {
	_, _, m := start(t)
	if r := stayCmd(t, m, studio, true); r["stay_still"] != true {
		t.Fatalf("studio on %v", r)
	}
	if r := stayCmd(t, m, studio, false); r["stay_still"] != false {
		t.Fatalf("studio off %v", r)
	}
}

// After a program the same control stream stays: not one release, not one new request.
func TestStayStillNoGapAfterProgram(t *testing.T) {
	robot, sink, m := start(t)
	stayCmd(t, m, ha, true)
	id := begin(t, m, map[string]any{"heartbeat": 5})
	if _, err := m.Command(context.Background(), "program_end", jsonArgs(t, map[string]any{"session": id, "reason": "end"})); err != nil {
		t.Fatal(err)
	}
	if !sink.has("program_ended") {
		t.Fatal("no program_ended")
	}
	time.Sleep(300 * time.Millisecond)
	robot.get(func() {
		if robot.controls != 1 || robot.releases != 0 {
			t.Fatalf("controls=%d releases=%d: a gap after the program", robot.controls, robot.releases)
		}
		if robot.stops < 1 {
			t.Fatal("motors not stopped at the end")
		}
	})
	if !m.Snapshot().StayStill || m.Snapshot().Control != "held" {
		t.Fatalf("after the program stay=%v control=%q", m.Snapshot().StayStill, m.Snapshot().Control)
	}
}

func TestProgramBeginSwitchesStayStill(t *testing.T) {
	robot, _, m := start(t)
	// option off (tests' Config): without the argument nothing changes
	id := begin(t, m, map[string]any{"heartbeat": 5})
	if m.StayStill() {
		t.Fatal("stay on without asking")
	}
	_, _ = m.Command(context.Background(), "program_end", jsonArgs(t, map[string]any{"session": id}))
	eventually(t, "release", func() (ok bool) {
		robot.get(func() { ok = robot.releases == 1 })
		return ok
	})
	// stay_still: true → after the program Vector stands still
	id = begin(t, m, map[string]any{"heartbeat": 5, "stay_still": true})
	if !m.StayStill() {
		t.Fatal("program_begin stay_still:true did not switch it on")
	}
	_, _ = m.Command(context.Background(), "program_end", jsonArgs(t, map[string]any{"session": id}))
	time.Sleep(300 * time.Millisecond)
	robot.get(func() {
		if robot.releases != 1 {
			t.Fatalf("released after a stand-still program (releases=%d)", robot.releases)
		}
	})
	// stay_still: false switches it off again
	id = begin(t, m, map[string]any{"heartbeat": 5, "stay_still": false})
	if m.StayStill() {
		t.Fatal("stay_still:false left it on")
	}
	_, _ = m.Command(context.Background(), "program_end", jsonArgs(t, map[string]any{"session": id}))
}

func TestProgramStayStillOption(t *testing.T) {
	_, _, m := startWith(t, func(c *Config) { c.ProgramStayStill = true })
	begin(t, m, map[string]any{"heartbeat": 5})
	if !m.StayStill() {
		t.Fatal("option program_stay_still ignored")
	}
}

func TestStayStillLetsGoOnChargerBehavior(t *testing.T) {
	_, sink, m := start(t)
	stayCmd(t, m, ha, true)
	m.endProgram("", "x", "") // nothing runs: no-op
	begin(t, m, map[string]any{"heartbeat": 5})
	if !m.keepStillAfterProgram("end") {
		t.Fatal("end must keep standing still")
	}
	if m.keepStillAfterProgram("charger") || m.StayStill() {
		t.Fatal("go_to_charger must let Vector go")
	}
	if e := sink.last("stay_still_ended"); e == nil || e.Data["reason"] != "charger" {
		t.Fatalf("stay_still_ended %+v", e)
	}
}

func TestStayStillTimeoutAndBattery(t *testing.T) {
	shortStay(t)
	robot, sink, m := startWith(t, func(c *Config) { c.StayStillTimeout = 200 * time.Millisecond })
	stayCmd(t, m, ha, true)
	eventually(t, "timeout", func() bool { return !m.StayStill() })
	if e := sink.last("stay_still_ended"); e == nil || e.Data["reason"] != "timeout" {
		t.Fatalf("stay_still_ended %+v", e)
	}
	eventually(t, "release", func() (ok bool) {
		robot.get(func() { ok = robot.releases >= 1 })
		return ok
	})

	_, sink2, m2 := start(t)
	stayCmd(t, m2, ha, true)
	m2.update(func(t *Tracker) bool { t.S.Battery.Level = "low"; return true })
	eventually(t, "battery", func() bool { return !m2.StayStill() })
	if e := sink2.last("stay_still_ended"); e == nil || e.Data["reason"] != "battery_low" {
		t.Fatalf("stay_still_ended %+v", e)
	}
	// low battery: refused
	if _, err := m2.Command(context.Background(), "stay_still", jsonArgs(t, map[string]any{"on": true})); !errors.Is(err, ErrBatteryLow) {
		t.Fatalf("on with a low battery: %v", err)
	}
}

// Vector takes control back (picked up): the keeper asks again.
func TestStayStillKeeperTakesControlBack(t *testing.T) {
	shortStay(t)
	robot, _, m := start(t)
	stayCmd(t, m, ha, true)
	robot.loseCtl <- struct{}{}
	eventually(t, "control again", func() (ok bool) {
		robot.get(func() { ok = robot.controls >= 2 })
		return ok && m.Snapshot().Control == "held"
	})
}

// The card releases control after driving: standing still keeps it.
func TestReleaseControlKeepsStayStill(t *testing.T) {
	robot, _, m := start(t)
	stayCmd(t, m, ha, true)
	if _, err := m.Command(WithCaller(context.Background(), ha), "release_control", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	robot.get(func() {
		if robot.releases != 0 {
			t.Fatalf("release_control let Vector go while standing still")
		}
	})
	if !m.StayStill() || m.Snapshot().Control != "held" {
		t.Fatal("standing still ended")
	}
}

func TestStayStillNeedsRobot(t *testing.T) {
	_, _, m := start(t)
	m2 := NewManager(Config{IP: "x", GUID: "y"}, m.log, &fakeSink{}, nil)
	if _, err := m2.Command(context.Background(), "stay_still", Args{"on": []byte("true")}); err == nil {
		t.Fatal("stay_still without a robot")
	}
}

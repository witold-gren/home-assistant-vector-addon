package robot

import (
	"testing"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

func robotState(status vectorpb.RobotStatus, touched bool) *vectorpb.Event {
	return &vectorpb.Event{EventType: &vectorpb.Event_RobotState{RobotState: &vectorpb.RobotState{
		Status:       uint32(status),
		LiftHeightMm: liftMinMM,
		TouchData:    &vectorpb.TouchData{IsBeingTouched: touched},
	}}}
}

func names(evs []Event) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Name)
	}
	return out
}

func TestRobotStateBitsAndEdges(t *testing.T) {
	tr := NewTracker("esn", "Vector", "ip")
	tr.ApplyBattery(&vectorpb.BatteryStateResponse{BatteryLevel: vectorpb.BatteryLevel_BATTERY_LEVEL_NOMINAL, BatteryVolts: 3.85})
	now := time.Unix(1000, 0)

	changed, evs := tr.ApplyEvent(robotState(vectorpb.RobotStatus_ROBOT_STATUS_IS_ON_CHARGER|vectorpb.RobotStatus_ROBOT_STATUS_IS_CHARGING, false), now)
	if !changed || !tr.S.Battery.OnCharger || !tr.S.Battery.Charging || len(evs) != 0 {
		t.Fatalf("charger bits: changed=%v battery=%+v events=%v", changed, tr.S.Battery, names(evs))
	}
	// same message again: nothing new (RobotState arrives ~17×/s)
	if changed, evs := tr.ApplyEvent(robotState(vectorpb.RobotStatus_ROBOT_STATUS_IS_ON_CHARGER|vectorpb.RobotStatus_ROBOT_STATUS_IS_CHARGING, false), now); changed || len(evs) != 0 {
		t.Fatalf("repeat changed=%v events=%v", changed, names(evs))
	}

	picked := vectorpb.RobotStatus_ROBOT_STATUS_IS_PICKED_UP | vectorpb.RobotStatus_ROBOT_STATUS_IS_BEING_HELD
	_, evs = tr.ApplyEvent(robotState(picked, false), now)
	if !tr.S.PickedUp || !tr.S.Held || tr.S.Battery.OnCharger || len(evs) != 1 || evs[0].Name != "picked_up" {
		t.Fatalf("picked up: %+v events=%v", tr.S, names(evs))
	}
	if _, evs = tr.ApplyEvent(robotState(picked, false), now); len(evs) != 0 {
		t.Fatalf("picked_up must fire once, got %v", names(evs))
	}

	_, evs = tr.ApplyEvent(robotState(vectorpb.RobotStatus_ROBOT_STATUS_CLIFF_DETECTED|vectorpb.RobotStatus_ROBOT_STATUS_IS_BUTTON_PRESSED, false), now)
	if got := names(evs); len(got) != 2 || got[0] != "cliff" || got[1] != "button_pressed" {
		t.Fatalf("cliff+button events: %v", got)
	}
	if _, evs = tr.ApplyEvent(robotState(vectorpb.RobotStatus_ROBOT_STATUS_IS_BUTTON_PRESSED, false), now); len(evs) != 0 {
		t.Fatalf("held button must not repeat: %v", names(evs))
	}

	tr.ApplyEvent(robotState(vectorpb.RobotStatus_ROBOT_STATUS_CALM_POWER_MODE, false), now)
	if !tr.S.Sleeping {
		t.Fatal("calm power mode = sleeping")
	}
}

func TestTouchDebounce(t *testing.T) {
	tr := NewTracker("esn", "Vector", "ip")
	now := time.Unix(1000, 0)
	_, evs := tr.ApplyEvent(robotState(0, true), now)
	if !tr.S.Touched || len(evs) != 1 || evs[0].Name != "touched" {
		t.Fatalf("touch start: %v %v", tr.S.Touched, names(evs))
	}
	// flicker off for a moment: still touched, no new event
	tr.ApplyEvent(robotState(0, false), now.Add(500*time.Millisecond))
	_, evs = tr.ApplyEvent(robotState(0, true), now.Add(time.Second))
	if !tr.S.Touched || len(evs) != 0 {
		t.Fatalf("flicker: %v %v", tr.S.Touched, names(evs))
	}
	if changed, _ := tr.Tick(now.Add(2 * time.Second)); changed {
		t.Fatal("released too early")
	}
	if changed, _ := tr.Tick(now.Add(2600 * time.Millisecond)); !changed || tr.S.Touched {
		t.Fatal("touch must end 1.5 s after the last contact")
	}
}

func TestFacesActivityWakeWord(t *testing.T) {
	tr := NewTracker("esn", "Vector", "ip")
	now := time.Unix(1000, 0)
	face := &vectorpb.Event{EventType: &vectorpb.Event_RobotObservedFace{RobotObservedFace: &vectorpb.RobotObservedFace{FaceId: 1, Name: "Alice"}}}
	if _, evs := tr.ApplyEvent(face, now); len(evs) != 1 || evs[0].Data["name"] != "Alice" {
		t.Fatalf("first face: %v", evs)
	}
	if _, evs := tr.ApplyEvent(face, now.Add(30*time.Second)); len(evs) != 0 {
		t.Fatalf("same face within a minute: %v", names(evs))
	}
	if _, evs := tr.ApplyEvent(face, now.Add(61*time.Second)); len(evs) != 1 {
		t.Fatalf("same face after a minute: %v", names(evs))
	}

	status := func(name string) *vectorpb.Event {
		return &vectorpb.Event{EventType: &vectorpb.Event_TimeStampedStatus{TimeStampedStatus: &vectorpb.TimeStampedStatus{
			Status: &vectorpb.Status{StatusType: &vectorpb.Status_FeatureStatus{FeatureStatus: &vectorpb.FeatureStatus{FeatureName: name}}}}}}
	}
	tr.ApplyEvent(status("Petting"), now)
	tr.ApplyEvent(status("NoFeature"), now)
	if tr.S.Activity != "Petting" {
		t.Fatalf("activity = %q, NoFeature must be skipped", tr.S.Activity)
	}

	begin := &vectorpb.Event{EventType: &vectorpb.Event_WakeWord{WakeWord: &vectorpb.WakeWord{WakeWordType: &vectorpb.WakeWord_WakeWordBegin{WakeWordBegin: &vectorpb.WakeWordBegin{}}}}}
	end := &vectorpb.Event{EventType: &vectorpb.Event_WakeWord{WakeWord: &vectorpb.WakeWord{WakeWordType: &vectorpb.WakeWord_WakeWordEnd{WakeWordEnd: &vectorpb.WakeWordEnd{IntentHeard: true}}}}}
	if _, evs := tr.ApplyEvent(begin, now); !tr.S.Listening || evs[0].Data["phase"] != "begin" {
		t.Fatal("wake word begin")
	}
	if _, evs := tr.ApplyEvent(end, now); tr.S.Listening || evs[0].Data["intent_heard"] != true {
		t.Fatal("wake word end")
	}

	stim := &vectorpb.Event{EventType: &vectorpb.Event_StimulationInfo{StimulationInfo: &vectorpb.StimulationInfo{Value: 0.94}}}
	tr.ApplyEvent(stim, now)
	if tr.S.Stimulation != 0.9 {
		t.Fatalf("stimulation = %v", tr.S.Stimulation)
	}
}

func TestBatteryPercent(t *testing.T) {
	for _, c := range []struct {
		level vectorpb.BatteryLevel
		volts float64
		want  int
	}{
		{vectorpb.BatteryLevel_BATTERY_LEVEL_FULL, 3.9, 100},
		{vectorpb.BatteryLevel_BATTERY_LEVEL_NOMINAL, 4.2, 100},
		{vectorpb.BatteryLevel_BATTERY_LEVEL_NOMINAL, 3.85, 50},
		{vectorpb.BatteryLevel_BATTERY_LEVEL_LOW, 3.5, 0},
	} {
		if got := BatteryPercent(c.level, c.volts); got != c.want {
			t.Errorf("%v %.2f V = %d, want %d", c.level, c.volts, got, c.want)
		}
	}
}

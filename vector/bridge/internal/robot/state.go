// Package robot keeps one Vector connected over gRPC, turns its event stream into a small state
// model plus discrete events, and runs commands (taking behavior control only while needed).
package robot

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

// Battery from BatteryState, with charger bits refreshed live from RobotState.
type Battery struct {
	Level     string  `json:"level"` // unknown | low | nominal | full
	Volts     float64 `json:"volts"`
	Percent   int     `json:"percent"` // estimated from volts (Vector reports no percentage)
	Charging  bool    `json:"charging"`
	OnCharger bool    `json:"on_charger"`
}

// Face is the last face Vector reported (name is empty for an unknown face).
type Face struct {
	Name string `json:"name"`
	ID   int32  `json:"id"`
	At   int64  `json:"at"`
}

// Photo stored on the robot (taken with "take a photo").
type Photo struct {
	ID uint32 `json:"id"`
	At int64  `json:"at"`
}

// Pose of the robot: millimetres and degrees, rounded (it changes all the time while driving).
type Pose struct {
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Angle float64 `json:"angle"`
}

// Cube is Vector's light cube.
type Cube struct {
	Connected    bool    `json:"connected"`
	ObjectID     uint32  `json:"object_id"`
	FactoryID    string  `json:"factory_id"`
	BatteryVolts float64 `json:"battery_volts"`
	BatteryLow   bool    `json:"battery_low"`
}

// KnownFace is a face Vector has enrolled.
type KnownFace struct {
	ID       int32  `json:"id"`
	Name     string `json:"name"`
	LastSeen int64  `json:"last_seen"`
}

// AudioState of the robot speaker as played by the bridge.
type AudioState struct {
	Playing  bool    `json:"playing"`
	Duration float64 `json:"duration"` // seconds of the current clip
	Started  int64   `json:"started"`
	Volume   int     `json:"volume"` // default clip volume 0–100
}

// EyeColor is the custom eye color last set through the bridge (Vector does not report it back).
type EyeColor struct {
	Hue        float64 `json:"hue"`
	Saturation float64 `json:"saturation"`
}

// State is what the integration sees: the hello message and every `state` event carry it whole.
type State struct {
	ESN         string         `json:"esn"`
	Name        string         `json:"name"`
	IP          string         `json:"ip"`
	Connected   bool           `json:"connected"`
	Firmware    string         `json:"firmware,omitempty"`
	Battery     *Battery       `json:"battery"`
	PickedUp    bool           `json:"picked_up"`
	Held        bool           `json:"held"`
	Cliff       bool           `json:"cliff"`
	Sleeping    bool           `json:"sleeping"`
	Touched     bool           `json:"touched"`
	Listening   bool           `json:"listening"`
	Activity    string         `json:"activity"`
	Stimulation float64        `json:"stimulation"`
	LastFace    *Face          `json:"last_face"`
	HeadAngle   float64        `json:"head_angle"`  // degrees, -22..45
	LiftHeight  float64        `json:"lift_height"` // percent, 0..100
	Settings    map[string]any `json:"settings"`
	Stats       map[string]any `json:"stats"`
	Control     string         `json:"control"` // none | auto | held
	Camera      bool           `json:"camera"`  // camera feed running
	Photos      int            `json:"photos"`  // photos stored on the robot
	LastPhoto   *Photo         `json:"last_photo"`
	Audio       AudioState     `json:"audio"`
	Pose        *Pose          `json:"pose"`  // in the robot's world frame (reset when picked up)
	Cube        *Cube          `json:"cube"`  // nil until a cube was seen or connected
	Faces       []KnownFace    `json:"faces"` // faces Vector knows by name
	CustomEye   *EyeColor      `json:"custom_eye"`
	Updated     int64          `json:"updated"`
}

// Event is a discrete happening pushed to the integration as it occurs.
type Event struct {
	Name string         `json:"event"`
	Data map[string]any `json:"data,omitempty"`
}

const (
	touchRelease = 1500 * time.Millisecond // touch sensor flickers while petting
	faceRepeat   = 60 * time.Second        // same face again → another face_seen only after this
	liftMinMM    = 32.0
	liftMaxMM    = 92.0
	voltsEmpty   = 3.6
	voltsFull    = 4.1
)

// Tracker folds raw robot messages into State. Not safe for concurrent use (Manager locks it).
type Tracker struct {
	S          State
	touchSeen  time.Time
	faceSeen   map[string]time.Time
	lastFaceID int32
	button     bool
}

func NewTracker(esn, name, ip string) *Tracker {
	return &Tracker{
		S:        State{ESN: esn, Name: name, IP: ip, Control: "none", Audio: AudioState{Volume: 80}, Settings: map[string]any{}, Stats: map[string]any{}},
		faceSeen: map[string]time.Time{},
	}
}

func setBool(dst *bool, v bool, changed *bool) {
	if *dst != v {
		*dst = v
		*changed = true
	}
}

func setFloat(dst *float64, v float64, changed *bool) {
	if *dst != v {
		*dst = v
		*changed = true
	}
}

func setString(dst *string, v string, changed *bool) {
	if *dst != v {
		*dst = v
		*changed = true
	}
}

// round to a multiple of step; a fractional step divides by 1/step so the result has no binary
// noise like 0.9500000000000001
func round(v, step float64) float64 {
	if step >= 1 {
		return math.Round(v/step) * step
	}
	n := math.Round(1 / step)
	return math.Round(v*n) / n
}

func has(status uint32, bit vectorpb.RobotStatus) bool { return status&uint32(bit) != 0 }

// ApplyEvent folds one EventStream message. Returns whether State changed and the events to push.
func (t *Tracker) ApplyEvent(ev *vectorpb.Event, now time.Time) (changed bool, out []Event) {
	switch {
	case ev.GetRobotState() != nil:
		return t.applyRobotState(ev.GetRobotState(), now)
	case ev.GetWakeWord() != nil:
		ww := ev.GetWakeWord()
		if ww.GetWakeWordBegin() != nil {
			setBool(&t.S.Listening, true, &changed)
			out = append(out, Event{Name: "wake_word", Data: map[string]any{"phase": "begin"}})
		} else if ww.GetWakeWordEnd() != nil {
			setBool(&t.S.Listening, false, &changed)
			end := ww.GetWakeWordEnd()
			data := map[string]any{"phase": "end", "intent_heard": end.GetIntentHeard()}
			if j := end.GetIntentJson(); j != "" {
				data["intent_json"] = j
			}
			out = append(out, Event{Name: "wake_word", Data: data})
		}
	case ev.GetRobotObservedFace() != nil:
		f := ev.GetRobotObservedFace()
		key := f.GetName()
		if key == "" {
			key = fmt.Sprintf("#%d", f.GetFaceId())
		}
		if f.GetFaceId() != t.lastFaceID || now.Sub(t.faceSeen[key]) >= faceRepeat {
			t.lastFaceID = f.GetFaceId()
			t.faceSeen[key] = now
			t.S.LastFace = &Face{Name: f.GetName(), ID: f.GetFaceId(), At: now.Unix()}
			changed = true
			out = append(out, Event{Name: "face_seen", Data: map[string]any{"name": f.GetName(), "face_id": f.GetFaceId()}})
		}
	case ev.GetTimeStampedStatus() != nil:
		name := ev.GetTimeStampedStatus().GetStatus().GetFeatureStatus().GetFeatureName()
		if name != "" && name != "NoFeature" {
			setString(&t.S.Activity, name, &changed)
		}
	case ev.GetStimulationInfo() != nil:
		setFloat(&t.S.Stimulation, round(float64(ev.GetStimulationInfo().GetValue()), 0.1), &changed)
	case ev.GetUserIntent() != nil:
		ui := ev.GetUserIntent()
		out = append(out, Event{Name: "intent", Data: map[string]any{"intent_id": ui.GetIntentId(), "json": ui.GetJsonData()}})
	case ev.GetObjectEvent().GetObjectTapped() != nil:
		out = append(out, Event{Name: "cube_tapped", Data: map[string]any{"object_id": ev.GetObjectEvent().GetObjectTapped().GetObjectId()}})
	case ev.GetObjectEvent().GetObjectConnectionState() != nil:
		cs := ev.GetObjectEvent().GetObjectConnectionState()
		c := t.cube()
		changed = c.Connected != cs.GetConnected() || c.ObjectID != cs.GetObjectId()
		c.Connected, c.ObjectID = cs.GetConnected(), cs.GetObjectId()
		if cs.GetFactoryId() != "" {
			c.FactoryID = cs.GetFactoryId()
		}
		name := "cube_disconnected"
		if cs.GetConnected() {
			name = "cube_connected"
		}
		out = append(out, Event{Name: name, Data: map[string]any{"factory_id": c.FactoryID}})
	case ev.GetObjectEvent().GetCubeConnectionLost() != nil:
		if c := t.S.Cube; c != nil && c.Connected {
			c.Connected, changed = false, true
			out = append(out, Event{Name: "cube_disconnected", Data: map[string]any{"factory_id": c.FactoryID}})
		}
	case ev.GetObjectEvent().GetObjectAvailable() != nil:
		if c := t.cube(); c.FactoryID == "" {
			c.FactoryID, changed = ev.GetObjectEvent().GetObjectAvailable().GetFactoryId(), true
		}
	}
	return changed, out
}

func (t *Tracker) applyRobotState(rs *vectorpb.RobotState, now time.Time) (changed bool, out []Event) {
	st := rs.GetStatus()
	pickedUp := has(st, vectorpb.RobotStatus_ROBOT_STATUS_IS_PICKED_UP)
	if pickedUp && !t.S.PickedUp {
		out = append(out, Event{Name: "picked_up"})
	}
	setBool(&t.S.PickedUp, pickedUp, &changed)
	setBool(&t.S.Held, has(st, vectorpb.RobotStatus_ROBOT_STATUS_IS_BEING_HELD), &changed)
	cliff := has(st, vectorpb.RobotStatus_ROBOT_STATUS_CLIFF_DETECTED)
	if cliff && !t.S.Cliff {
		out = append(out, Event{Name: "cliff"})
	}
	setBool(&t.S.Cliff, cliff, &changed)
	setBool(&t.S.Sleeping, has(st, vectorpb.RobotStatus_ROBOT_STATUS_CALM_POWER_MODE), &changed)
	// the button bit stays set while pressed: one event per press, no state field
	button := has(st, vectorpb.RobotStatus_ROBOT_STATUS_IS_BUTTON_PRESSED)
	if button && !t.button {
		out = append(out, Event{Name: "button_pressed"})
	}
	t.button = button
	if b := t.S.Battery; b != nil {
		setBool(&b.Charging, has(st, vectorpb.RobotStatus_ROBOT_STATUS_IS_CHARGING), &changed)
		setBool(&b.OnCharger, has(st, vectorpb.RobotStatus_ROBOT_STATUS_IS_ON_CHARGER), &changed)
	}

	if rs.GetTouchData().GetIsBeingTouched() {
		t.touchSeen = now
		if !t.S.Touched {
			t.S.Touched = true
			changed = true
			out = append(out, Event{Name: "touched"})
		}
	}

	if p := rs.GetPose(); p != nil {
		np := Pose{X: round(float64(p.GetX()), 10), Y: round(float64(p.GetY()), 10), Angle: round(float64(rs.GetPoseAngleRad())*180/math.Pi, 5)}
		if t.S.Pose == nil || *t.S.Pose != np {
			t.S.Pose, changed = &np, true
		}
	}
	setFloat(&t.S.HeadAngle, math.Round(float64(rs.GetHeadAngleRad())*180/math.Pi), &changed)
	lift := (float64(rs.GetLiftHeightMm()) - liftMinMM) / (liftMaxMM - liftMinMM) * 100
	setFloat(&t.S.LiftHeight, round(math.Max(0, math.Min(100, lift)), 5), &changed)

	c2, out2 := t.Tick(now)
	return changed || c2, append(out, out2...)
}

// Tick ends a touch after touchRelease without contact. Called by every RobotState and by a timer.
func (t *Tracker) Tick(now time.Time) (changed bool, out []Event) {
	if t.S.Touched && now.Sub(t.touchSeen) >= touchRelease {
		t.S.Touched = false
		changed = true
	}
	return changed, out
}

var batteryLevels = map[vectorpb.BatteryLevel]string{
	vectorpb.BatteryLevel_BATTERY_LEVEL_UNKNOWN: "unknown",
	vectorpb.BatteryLevel_BATTERY_LEVEL_LOW:     "low",
	vectorpb.BatteryLevel_BATTERY_LEVEL_NOMINAL: "nominal",
	vectorpb.BatteryLevel_BATTERY_LEVEL_FULL:    "full",
}

// BatteryPercent estimates charge from voltage (linear between empty and full; "full" level = 100).
func BatteryPercent(level vectorpb.BatteryLevel, volts float64) int {
	if level == vectorpb.BatteryLevel_BATTERY_LEVEL_FULL {
		return 100
	}
	p := (volts - voltsEmpty) / (voltsFull - voltsEmpty) * 100
	return int(math.Round(math.Max(0, math.Min(100, p))))
}

// ApplyBattery folds a BatteryState response.
func (t *Tracker) ApplyBattery(b *vectorpb.BatteryStateResponse) bool {
	nb := &Battery{
		Level:     batteryLevels[b.GetBatteryLevel()],
		Volts:     round(float64(b.GetBatteryVolts()), 0.01),
		Percent:   BatteryPercent(b.GetBatteryLevel(), float64(b.GetBatteryVolts())),
		Charging:  b.GetIsCharging(),
		OnCharger: b.GetIsOnChargerPlatform(),
	}
	changed := false
	if cb := b.GetCubeBattery(); cb != nil && cb.GetFactoryId() != "" {
		c := t.cube()
		v, low := round(float64(cb.GetBatteryVolts()), 0.01), cb.GetLevel() == vectorpb.CubeBattery_Low
		if c.BatteryVolts != v || c.BatteryLow != low || c.FactoryID != cb.GetFactoryId() {
			c.BatteryVolts, c.BatteryLow, c.FactoryID, changed = v, low, cb.GetFactoryId(), true
		}
	}
	if t.S.Battery != nil && *t.S.Battery == *nb {
		return changed
	}
	t.S.Battery = nb
	return true
}

func (t *Tracker) cube() *Cube {
	if t.S.Cube == nil {
		t.S.Cube = &Cube{}
	}
	return t.S.Cube
}

// ApplyJdocs folds ROBOT_SETTINGS and ROBOT_LIFETIME_STATS documents.
func (t *Tracker) ApplyJdocs(r *vectorpb.PullJdocsResponse) bool {
	changed := false
	for _, nd := range r.GetNamedJdocs() {
		var m map[string]any
		if err := json.Unmarshal([]byte(nd.GetDoc().GetJsonDoc()), &m); err != nil {
			continue
		}
		switch nd.GetJdocType() {
		case vectorpb.JdocType_ROBOT_SETTINGS:
			t.S.Settings, changed = m, true
		case vectorpb.JdocType_ROBOT_LIFETIME_STATS:
			t.S.Stats, changed = m, true
		}
	}
	return changed
}

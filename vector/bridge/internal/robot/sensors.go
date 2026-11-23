package robot

import (
	"math"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

// Sensors for a program (`sensors` command): read at once from the tracker, without waiting for
// the 1-second state push. Units are Vector's: millimetres, degrees, mm/s; angles + = left.
type Sensors struct {
	At        float64 `json:"at"` // unix seconds of the last RobotState (ms precision)
	Connected bool    `json:"connected"`

	Touched   bool `json:"touched"`   // back sensor, held 1.5 s after the last contact
	PickedUp  bool `json:"picked_up"` // lifted off the ground
	Held      bool `json:"held"`      // held in a hand
	Falling   bool `json:"falling"`
	Cliff     bool `json:"cliff"` // a table edge under the front
	Moving    bool `json:"moving"`
	Listening bool `json:"listening"` // after "Hey Vector"

	// Time-of-flight distance sensor at the front. ObstacleOK: an object was found in the valid
	// range and the lift does not cover the sensor — only then is ObstacleMM a real distance.
	ObstacleMM     int     `json:"obstacle_mm"`
	ObstacleOK     bool    `json:"obstacle_ok"`
	ObstacleClear  bool    `json:"obstacle_clear"` // nothing up to the sensor's maximum range
	ObstacleSignal float64 `json:"obstacle_signal"`
	LiftInView     bool    `json:"lift_in_fov"` // the lift (or a cube on it) covers the sensor

	OnCharger  bool     `json:"on_charger"`
	Charging   bool     `json:"charging"`
	Battery    *Battery `json:"battery"`
	HeadAngle  float64  `json:"head_angle"`  // degrees, -22..45
	LiftHeight float64  `json:"lift_height"` // percent, 0..100
	Pose       *Pose    `json:"pose"`

	Face       *SensorFace   `json:"face"` // the last face seen (nil = none yet)
	Cube       SensorCube    `json:"cube"`
	LastSpeech *SensorSpeech `json:"last_speech"` // the last sentence heard after "Hey Vector"

	Accel  Vec3   `json:"accel"`  // mm/s²
	Gyro   Vec3   `json:"gyro"`   // rad/s
	Wheels Wheels `json:"wheels"` // measured wheel speeds, mm/s

	// Counts since the program session began (or since the add-on started when none runs).
	Counts Counters `json:"counts"`

	Stimulation float64 `json:"stimulation"` // how excited Vector is, 0–1 (StimulationInfo)
	Volume      *int    `json:"volume"`      // master volume 0 (mute) – 5 (high); nil = not read yet
	Life        Life    `json:"life"`        // lifetime statistics (refreshed every 10 minutes)
}

// Life: Vector's lifetime statistics (ROBOT_LIFETIME_STATS); nil = not in the document.
type Life struct {
	AliveDays  *float64 `json:"alive_days"`  // days since the robot's first start
	PetMinutes *float64 `json:"pet_minutes"` // minutes of petting
	DistanceM  *float64 `json:"distance_m"`  // metres driven
	WakeWords  *float64 `json:"wake_words"`  // times it reacted to "Hey Vector"
}

// lifeStat: a number of the stats document times factor, rounded to step.
func lifeStat(stats map[string]any, key string, factor, step float64) *float64 {
	v, ok := stats[key].(float64)
	if !ok {
		return nil
	}
	r := round(v*factor, step)
	return &r
}

// SensorFace: the last face Vector saw; AgeS = seconds since (≤ 2 = "sees a face now").
type SensorFace struct {
	ID   int32   `json:"id"`
	Name string  `json:"name"` // empty for a face Vector does not know
	AgeS float64 `json:"age_s"`
}

// SensorCube: Visible = seen by the camera within the last second; SeenAgeS nil = never seen.
type SensorCube struct {
	Connected bool     `json:"connected"`
	Visible   bool     `json:"visible"`
	SeenAgeS  *float64 `json:"seen_age_s"`
	Taps      int      `json:"taps"` // since the session began (same as counts.cube_taps)
}

// SensorSpeech: what wire-pod heard after "Hey Vector" (the `speech` event), AgeS seconds ago.
type SensorSpeech struct {
	Text string  `json:"text"`
	AgeS float64 `json:"age_s"`
}

type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

type Wheels struct {
	Left  float64 `json:"left"`
	Right float64 `json:"right"`
}

// Counters of discrete events (latched: a program asks "how many since I last looked").
type Counters struct {
	Touches       int `json:"touches"`
	ButtonPresses int `json:"button_presses"`
	PickedUp      int `json:"picked_up"`
	Cliff         int `json:"cliff"`
	WakeWords     int `json:"wake_words"`
	Speech        int `json:"speech"`
	CubeTaps      int `json:"cube_taps"`
}

const cubeVisibleFor = time.Second

// liveSensors: the raw values of the last RobotState and the event counters.
type liveSensors struct {
	at     time.Time
	status uint32
	prox   struct {
		mm                 uint32
		signal             float32
		clear, found, lift bool
	}
	accel    Vec3
	gyro     Vec3
	wheels   Wheels
	cubeSeen time.Time
	faceAt   time.Time // every sighting (face_seen events are rate limited)

	activitySeq int // changes of the robot's active feature (run_behavior watches it)

	counts     Counters
	speech     string
	speechAt   time.Time
	cliffAt    time.Time // last cliff / pick-up / wake word: why control was lost
	pickedAt   time.Time
	wakeWordAt time.Time
}

func (l *liveSensors) apply(rs *vectorpb.RobotState, now time.Time) {
	l.at = now
	l.status = rs.GetStatus()
	if p := rs.GetProxData(); p != nil {
		l.prox.mm, l.prox.signal = p.GetDistanceMm(), p.GetSignalQuality()
		l.prox.clear, l.prox.found, l.prox.lift = p.GetUnobstructed(), p.GetFoundObject(), p.GetIsLiftInFov()
	}
	if a := rs.GetAccel(); a != nil {
		l.accel = Vec3{round(float64(a.GetX()), 1), round(float64(a.GetY()), 1), round(float64(a.GetZ()), 1)}
	}
	if g := rs.GetGyro(); g != nil {
		l.gyro = Vec3{round(float64(g.GetX()), 0.001), round(float64(g.GetY()), 0.001), round(float64(g.GetZ()), 0.001)}
	}
	l.wheels = Wheels{round(float64(rs.GetLeftWheelSpeedMmps()), 1), round(float64(rs.GetRightWheelSpeedMmps()), 1)}
}

// countEvents latches the events of one message into the counters.
func (t *Tracker) countEvents(events []Event, now time.Time) {
	c := &t.live.counts
	for _, e := range events {
		switch e.Name {
		case "touched":
			c.Touches++
		case "button_pressed":
			c.ButtonPresses++
		case "picked_up":
			c.PickedUp++
			t.live.pickedAt = now
		case "cliff":
			c.Cliff++
			t.live.cliffAt = now
		case "wake_word":
			if e.Data["phase"] == "begin" {
				c.WakeWords++
				t.live.wakeWordAt = now
			}
		case "cube_tapped":
			c.CubeTaps++
		}
	}
}

// NoteSpeech records a sentence heard after "Hey Vector" (the voice router's `speech` event).
func (t *Tracker) NoteSpeech(text string, now time.Time) {
	t.live.speech, t.live.speechAt = text, now
	t.live.counts.Speech++
}

// ResetCounts starts the counters again (a program session begins).
func (t *Tracker) ResetCounts() { t.live.counts = Counters{} }

func age(now, then time.Time) float64 { return round(now.Sub(then).Seconds(), 0.1) }

// Sensors builds the instant reading.
func (t *Tracker) Sensors(now time.Time) Sensors {
	l := &t.live
	s := Sensors{
		Connected: t.S.Connected,
		Touched:   t.S.Touched,
		PickedUp:  t.S.PickedUp,
		Held:      t.S.Held,
		Falling:   has(l.status, vectorpb.RobotStatus_ROBOT_STATUS_IS_FALLING),
		Cliff:     t.S.Cliff,
		Moving: has(l.status, vectorpb.RobotStatus_ROBOT_STATUS_IS_MOVING) ||
			has(l.status, vectorpb.RobotStatus_ROBOT_STATUS_ARE_WHEELS_MOVING),
		Listening:      t.S.Listening,
		ObstacleMM:     int(l.prox.mm),
		ObstacleOK:     l.prox.found && !l.prox.lift,
		ObstacleClear:  l.prox.clear,
		ObstacleSignal: round(float64(l.prox.signal), 0.01),
		LiftInView:     l.prox.lift,
		HeadAngle:      t.S.HeadAngle,
		LiftHeight:     t.S.LiftHeight,
		Accel:          l.accel,
		Gyro:           l.gyro,
		Wheels:         l.wheels,
		Counts:         l.counts,
	}
	if !l.at.IsZero() {
		s.At = math.Round(float64(l.at.UnixMilli())) / 1000
	}
	if b := t.S.Battery; b != nil {
		bc := *b
		s.Battery, s.OnCharger, s.Charging = &bc, b.OnCharger, b.Charging
	}
	if p := t.S.Pose; p != nil {
		pc := *p
		s.Pose = &pc
	}
	if f := t.S.LastFace; f != nil {
		seen := l.faceAt
		if seen.IsZero() {
			seen = time.Unix(f.At, 0)
		}
		s.Face = &SensorFace{ID: f.ID, Name: f.Name, AgeS: age(now, seen)}
	}
	if c := t.S.Cube; c != nil {
		s.Cube.Connected = c.Connected
	}
	if !l.cubeSeen.IsZero() {
		a := age(now, l.cubeSeen)
		s.Cube.SeenAgeS = &a
		s.Cube.Visible = now.Sub(l.cubeSeen) <= cubeVisibleFor
	}
	s.Cube.Taps = l.counts.CubeTaps
	if l.speech != "" {
		s.LastSpeech = &SensorSpeech{Text: l.speech, AgeS: age(now, l.speechAt)}
	}
	s.Stimulation = t.S.Stimulation
	if v, ok := t.S.Settings["master_volume"].(float64); ok {
		vi := int(v)
		s.Volume = &vi
	}
	s.Life = Life{
		AliveDays:  lifeStat(t.S.Stats, "Alive.seconds", 1.0/86400, 0.1),
		PetMinutes: lifeStat(t.S.Stats, "Pet.ms", 1.0/60000, 0.1),
		DistanceM:  lifeStat(t.S.Stats, "Odom.Body", 1e-9, 0.1), // nanometres
		WakeWords:  lifeStat(t.S.Stats, "BStat.ReactedToTriggerWord", 1, 1),
	}
	return s
}

// lossCause guesses why Vector took behavior control back: what happened in the last 3 s.
func (t *Tracker) lossCause(now time.Time) string {
	recent := func(at time.Time) bool { return !at.IsZero() && now.Sub(at) <= 3*time.Second }
	l := &t.live
	switch {
	case t.S.Cliff || recent(l.cliffAt):
		return "cliff"
	case has(l.status, vectorpb.RobotStatus_ROBOT_STATUS_IS_FALLING):
		return "falling"
	case t.S.PickedUp || recent(l.pickedAt):
		return "picked_up"
	case t.S.Listening || recent(l.wakeWordAt):
		return "wake_word"
	}
	return "robot"
}

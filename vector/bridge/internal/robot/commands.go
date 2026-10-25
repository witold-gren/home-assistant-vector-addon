package robot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

// Action id tags must come from the SDK range, or the robot answers "Invalid Action tag_id".
const (
	sdkTagFirst = 2000001
	sdkTagLast  = 3000000
)

func (m *Manager) nextTag() int32 {
	for {
		t := m.tags.Add(1)
		if t <= sdkTagLast {
			return t
		}
		m.tags.CompareAndSwap(t, sdkTagFirst-1)
	}
}

// Args of a command, decoded on demand.
type Args map[string]json.RawMessage

func (a Args) str(key, def string) string {
	var v string
	if raw, ok := a[key]; ok && json.Unmarshal(raw, &v) == nil {
		return v
	}
	return def
}

func (a Args) num(key string, def float64) float64 {
	var v float64
	if raw, ok := a[key]; ok && json.Unmarshal(raw, &v) == nil {
		return v
	}
	return def
}

func (a Args) boolean(key string, def bool) bool {
	var v bool
	if raw, ok := a[key]; ok && json.Unmarshal(raw, &v) == nil {
		return v
	}
	return def
}

func (a Args) has(key string) bool { _, ok := a[key]; return ok }

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func deg2rad(d float64) float32 { return float32(d * math.Pi / 180) }

// Command runs one bridge command; the result is sent back to the integration as JSON.
func (m *Manager) Command(ctx context.Context, name string, args Args) (any, error) {
	cli, err := m.client()
	if err != nil {
		return nil, err
	}
	switch name {
	case "refresh":
		m.refreshBattery(ctx, cli)
		m.refreshJdocs(ctx, cli)
		return m.Snapshot(), nil
	case "move":
		return nil, m.move(ctx, cli, args)
	case "stop":
		m.mover.mu.Lock()
		if m.mover.done != nil {
			m.stopMotorsLocked(cli)
		}
		m.mover.mu.Unlock()
		m.cancelAction()
		_, err := cli.StopAllMotors(ctx, &vectorpb.StopAllMotorsRequest{})
		m.lease.Release()
		return nil, err
	case "hold_control":
		d := time.Duration(clamp(args.num("seconds", 300), 1, 600)) * time.Second
		return nil, m.lease.Hold(ctx, d)
	case "release_control":
		m.lease.Release()
		return nil, nil
	case "list_animations":
		return m.listAnimations(ctx, cli, false)
	case "list_animation_triggers":
		return m.listAnimations(ctx, cli, true)
	case "app_intent":
		intent := args.str("intent", "")
		if intent == "" {
			return nil, errors.New("intent required")
		}
		_, err := cli.AppIntent(ctx, &vectorpb.AppIntentRequest{Intent: intent, Param: args.str("param", "")})
		if err == nil && strings.HasPrefix(intent, "intent_photo") {
			m.pollPhotosSoon(cli)
		}
		return nil, err
	case "list_photos":
		return listPhotos(ctx, cli)
	case "delete_photo":
		id := args.num("id", -1)
		if id < 0 {
			return nil, errors.New("id required")
		}
		r, err := cli.DeletePhoto(ctx, &vectorpb.DeletePhotoRequest{PhotoId: uint32(id)})
		if err != nil {
			return nil, err
		}
		if !r.GetSuccess() {
			return nil, ErrNoPhoto
		}
		m.refreshPhotos(ctx, cli)
		return nil, nil
	case "stop_audio":
		m.Player.Stop()
		return nil, nil
	case "set_audio_volume":
		m.Player.SetVolume(int(clamp(args.num("volume", 80), 0, 100)))
		return nil, nil
	case "camera_snapshot":
		// a fresh frame into the cache (cached snapshots never start the camera)
		if _, err := m.Camera.Fresh(ctx, 0); err != nil {
			return nil, err
		}
		_, at := m.Camera.Last()
		return map[string]any{"at": at.Unix()}, nil
	case "set_eye_color":
		hue, sat := clamp(args.num("hue", 0), 0, 1), clamp(args.num("saturation", 1), 0, 1)
		if _, err := cli.SetEyeColor(ctx, &vectorpb.SetEyeColorRequest{Hue: float32(hue), Saturation: float32(sat)}); err != nil {
			return nil, err
		}
		m.update(func(t *Tracker) bool { t.S.CustomEye = &EyeColor{Hue: hue, Saturation: sat}; return true })
		return nil, nil
	case "update_settings":
		return nil, m.updateSettings(ctx, cli, args)
	case "say_text":
		// wire-pod speaks with its own TTS (Piper over Wyoming) and needs no behavior control;
		// engine "vector" = Vector's built-in English voice over gRPC (an action, takes control)
		if args.str("engine", "wirepod") == "wirepod" && m.cfg.Wirepod != nil {
			text := strings.TrimSpace(args.str("text", ""))
			if text == "" {
				return nil, errors.New("text required")
			}
			if len([]rune(text)) >= 600 {
				return nil, errors.New("text too long (wire-pod accepts < 600 characters)")
			}
			return nil, m.cfg.Wirepod.SayText(ctx, m.ESN(), text)
		}
	}
	if res, err, ok := m.extraCommand(ctx, cli, name, args); ok {
		return res, err
	}
	if act, ok := actions[name]; ok {
		return m.runAction(ctx, cli, act, args)
	}
	return nil, fmt.Errorf("unknown command %q", name)
}

// An action moves or animates the robot: it needs behavior control and runs alone.
type action func(ctx context.Context, m *Manager, cli vectorpb.ExternalInterfaceClient, a Args) (any, error)

var actions = map[string]action{
	"say_text": func(ctx context.Context, _ *Manager, cli vectorpb.ExternalInterfaceClient, a Args) (any, error) {
		text := strings.TrimSpace(a.str("text", ""))
		if text == "" {
			return nil, errors.New("text required")
		}
		r, err := cli.SayText(ctx, &vectorpb.SayTextRequest{Text: text, UseVectorVoice: a.boolean("vector_voice", true), DurationScalar: float32(clamp(a.num("speed", 1), 0.5, 2))})
		if err != nil {
			return nil, err
		}
		return map[string]any{"state": r.GetState().String()}, nil
	},
	"play_animation": func(ctx context.Context, _ *Manager, cli vectorpb.ExternalInterfaceClient, a Args) (any, error) {
		r, err := cli.PlayAnimation(ctx, &vectorpb.PlayAnimationRequest{Animation: &vectorpb.Animation{Name: a.str("name", "")}, Loops: uint32(clamp(a.num("loops", 1), 1, 10))})
		return behaviorResult(r.GetResult(), err)
	},
	"play_animation_trigger": func(ctx context.Context, _ *Manager, cli vectorpb.ExternalInterfaceClient, a Args) (any, error) {
		r, err := cli.PlayAnimationTrigger(ctx, &vectorpb.PlayAnimationTriggerRequest{AnimationTrigger: &vectorpb.AnimationTrigger{Name: a.str("name", "")}, Loops: uint32(clamp(a.num("loops", 1), 1, 10)), UseLiftSafe: true})
		return behaviorResult(r.GetResult(), err)
	},
	"drive_on_charger": func(ctx context.Context, _ *Manager, cli vectorpb.ExternalInterfaceClient, _ Args) (any, error) {
		r, err := cli.DriveOnCharger(ctx, &vectorpb.DriveOnChargerRequest{})
		return behaviorResult(r.GetResult(), err)
	},
	"drive_off_charger": func(ctx context.Context, _ *Manager, cli vectorpb.ExternalInterfaceClient, _ Args) (any, error) {
		r, err := cli.DriveOffCharger(ctx, &vectorpb.DriveOffChargerRequest{})
		return behaviorResult(r.GetResult(), err)
	},
	"drive_straight": func(ctx context.Context, m *Manager, cli vectorpb.ExternalInterfaceClient, a Args) (any, error) {
		r, err := cli.DriveStraight(ctx, &vectorpb.DriveStraightRequest{DistMm: float32(clamp(a.num("distance", 100), -1000, 1000)), SpeedMmps: float32(clamp(a.num("speed", 100), 10, 200)), ShouldPlayAnimation: false, IdTag: m.nextTag()})
		return actionResult(r.GetResult(), err)
	},
	"turn_in_place": func(ctx context.Context, m *Manager, cli vectorpb.ExternalInterfaceClient, a Args) (any, error) {
		r, err := cli.TurnInPlace(ctx, &vectorpb.TurnInPlaceRequest{AngleRad: deg2rad(clamp(a.num("angle", 90), -360, 360)), SpeedRadPerSec: deg2rad(clamp(a.num("speed", 90), 10, 360)), AccelRadPerSec2: deg2rad(720), TolRad: deg2rad(2), IdTag: m.nextTag()})
		return actionResult(r.GetResult(), err)
	},
	"set_head_angle": func(ctx context.Context, m *Manager, cli vectorpb.ExternalInterfaceClient, a Args) (any, error) {
		r, err := cli.SetHeadAngle(ctx, &vectorpb.SetHeadAngleRequest{AngleRad: deg2rad(clamp(a.num("angle", 10), -22, 45)), MaxSpeedRadPerSec: 3, AccelRadPerSec2: 10, IdTag: m.nextTag()})
		return actionResult(r.GetResult(), err)
	},
	"set_lift_height": func(ctx context.Context, m *Manager, cli vectorpb.ExternalInterfaceClient, a Args) (any, error) {
		mm := liftMinMM + clamp(a.num("height", 0), 0, 100)/100*(liftMaxMM-liftMinMM)
		r, err := cli.SetLiftHeight(ctx, &vectorpb.SetLiftHeightRequest{HeightMm: float32(mm), MaxSpeedRadPerSec: 3, AccelRadPerSec2: 10, IdTag: m.nextTag()})
		return actionResult(r.GetResult(), err)
	},
	"drive_wheels": func(ctx context.Context, _ *Manager, cli vectorpb.ExternalInterfaceClient, a Args) (any, error) {
		left, right := float32(clamp(a.num("left", 0), -220, 220)), float32(clamp(a.num("right", 0), -220, 220))
		d := time.Duration(clamp(a.num("duration", 1), 0.1, 10) * float64(time.Second))
		if _, err := cli.DriveWheels(ctx, &vectorpb.DriveWheelsRequest{LeftWheelMmps: left, RightWheelMmps: right, LeftWheelMmps2: 200, RightWheelMmps2: 200}); err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
		case <-time.After(d):
		}
		// stop even when cancelled: never leave the wheels running
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := cli.DriveWheels(sctx, &vectorpb.DriveWheelsRequest{})
		return nil, err
	},
}

func behaviorResult(r vectorpb.BehaviorResults, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	if r != vectorpb.BehaviorResults_BEHAVIOR_COMPLETE_STATE {
		return nil, fmt.Errorf("robot: %s", r)
	}
	return nil, nil
}

func actionResult(r *vectorpb.ActionResult, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	if c := r.GetCode(); c != vectorpb.ActionResult_ACTION_RESULT_SUCCESS {
		return nil, fmt.Errorf("robot: %s", c)
	}
	return nil, nil
}

// runAction takes control, runs `act` alone (a new action waits for the previous one), and lets
// `stop` cancel it.
func (m *Manager) runAction(ctx context.Context, cli vectorpb.ExternalInterfaceClient, act action, args Args) (any, error) {
	m.action.Lock()
	defer m.action.Unlock()
	actx, cancel := context.WithCancel(ctx)
	defer cancel()
	m.actMu.Lock()
	m.actCtl = cancel
	m.actMu.Unlock()
	defer func() {
		m.actMu.Lock()
		m.actCtl = nil
		m.actMu.Unlock()
	}()

	done, lost, err := m.lease.Acquire(actx)
	if err != nil {
		return nil, err
	}
	defer done()
	go func() {
		select {
		case <-lost:
			cancel()
		case <-actx.Done():
		}
	}()
	res, err := act(actx, m, cli, args)
	// cancelled by `stop` or by losing control (not by the caller): report why
	if actx.Err() != nil && ctx.Err() == nil {
		select {
		case <-lost:
			return nil, ErrControlLost
		default:
			return nil, errors.New("stopped")
		}
	}
	return res, err
}

func (m *Manager) cancelAction() {
	m.actMu.Lock()
	defer m.actMu.Unlock()
	if m.actCtl != nil {
		m.actCtl()
	}
}

func (m *Manager) listAnimations(ctx context.Context, cli vectorpb.ExternalInterfaceClient, triggers bool) ([]string, error) {
	m.mu.Lock()
	cached := m.animations
	if triggers {
		cached = m.triggers
	}
	m.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	var names []string
	if triggers {
		r, err := cli.ListAnimationTriggers(ctx, &vectorpb.ListAnimationTriggersRequest{})
		if err != nil {
			return nil, err
		}
		for _, a := range r.GetAnimationTriggerNames() {
			names = append(names, a.GetName())
		}
	} else {
		r, err := cli.ListAnimations(ctx, &vectorpb.ListAnimationsRequest{})
		if err != nil {
			return nil, err
		}
		for _, a := range r.GetAnimationNames() {
			names = append(names, a.GetName())
		}
	}
	sort.Strings(names)
	m.mu.Lock()
	if triggers {
		m.triggers = names
	} else {
		m.animations = names
	}
	m.mu.Unlock()
	return names, nil
}

// Volume names as the integration's select shows them, index = Vector's Volume enum.
var Volumes = []string{"mute", "low", "medium_low", "medium", "medium_high", "high"}

// EyeColors (preset), index = Vector's EyeColor enum.
var EyeColors = []string{"teal", "orange", "yellow", "lime", "sapphire", "purple", "green"}

func indexOf(list []string, v string) int {
	for i, s := range list {
		if s == v {
			return i
		}
	}
	return -1
}

// updateSettings writes robot settings through the robot's REST gateway, like wire-pod does:
// POST https://<robot>/v1/update_settings {"update_settings": true, "settings": {...}}. The gRPC
// UpdateSettings answers OK but the robot does not keep the change (seen: time_zone stayed).
func (m *Manager) updateSettings(ctx context.Context, cli vectorpb.ExternalInterfaceClient, a Args) error {
	settings := map[string]any{}
	if a.has("master_volume") {
		i := indexOf(Volumes, a.str("master_volume", ""))
		if i < 0 {
			return errors.New("master_volume: one of " + strings.Join(Volumes, ", "))
		}
		settings["master_volume"] = i
	}
	if a.has("eye_color") {
		i := indexOf(EyeColors, a.str("eye_color", ""))
		if i < 0 {
			return errors.New("eye_color: one of " + strings.Join(EyeColors, ", "))
		}
		settings["eye_color"] = i
	}
	for _, k := range []string{"locale", "time_zone", "default_location"} {
		if a.has(k) {
			settings[k] = a.str(k, "")
		}
	}
	for _, k := range []string{"dist_is_metric", "temp_is_fahrenheit", "clock_24_hour"} {
		if a.has(k) {
			settings[k] = a.boolean(k, false)
		}
	}
	if a.has("button_wakeword") {
		w := 0
		if a.str("button_wakeword", "") == "alexa" {
			w = 1
		}
		settings["button_wakeword"] = w
	}
	if len(settings) == 0 {
		return errors.New("no setting given")
	}
	if err := m.rest(ctx, "/v1/update_settings", map[string]any{"update_settings": true, "settings": settings}); err != nil {
		return err
	}
	if _, ok := settings["eye_color"]; ok {
		m.update(func(t *Tracker) bool { t.S.CustomEye = nil; return true })
	}
	// the robot needs a moment before the settings document shows the change
	time.Sleep(500 * time.Millisecond)
	m.refreshJdocs(ctx, cli)
	return nil
}

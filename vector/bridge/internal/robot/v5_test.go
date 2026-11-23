package robot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"

	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/ai"
	hass "github.com/witold-gren/home-assistant-vector-addon/bridge/internal/ha"
	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/wirepod"
	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/wyoming"
)

// ---- fakes ----

// app intents and mirror mode on the fake robot; an app intent may play a short behavior
// (activity changes on the event stream) when behave is set.
func (f *fakeRobot) AppIntent(_ context.Context, r *vectorpb.AppIntentRequest) (*vectorpb.AppIntentResponse, error) {
	f.mu.Lock()
	f.intents = append(f.intents, r.GetIntent())
	f.holdingAtIntent = append(f.holdingAtIntent, f.holding)
	behave := f.behave
	f.mu.Unlock()
	if behave {
		go func() {
			for _, name := range []string{"Dancing", "Observing"} {
				f.events <- &vectorpb.Event{EventType: &vectorpb.Event_TimeStampedStatus{TimeStampedStatus: &vectorpb.TimeStampedStatus{
					Status: &vectorpb.Status{StatusType: &vectorpb.Status_FeatureStatus{FeatureStatus: &vectorpb.FeatureStatus{FeatureName: name}}}}}}
				time.Sleep(150 * time.Millisecond)
			}
		}()
	}
	return &vectorpb.AppIntentResponse{}, nil
}

func (f *fakeRobot) EnableMirrorMode(_ context.Context, r *vectorpb.EnableMirrorModeRequest) (*vectorpb.EnableMirrorModeResponse, error) {
	f.mu.Lock()
	f.mirror = append(f.mirror, r.GetEnable())
	f.mu.Unlock()
	return &vectorpb.EnableMirrorModeResponse{}, nil
}

// fakeTTS: Piper in memory. The "WAV" is a decimal PCM size (the test decoder's format).
type fakeTTS struct {
	mu     sync.Mutex
	asked  []string
	voices []string
	fail   error
}

func (f *fakeTTS) Voices(context.Context) ([]wyoming.Voice, error) {
	return []wyoming.Voice{{Name: "pl_PL-gosia-medium", Languages: []string{"pl_PL"}}}, nil
}

func (f *fakeTTS) Synthesize(_ context.Context, text, voice string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked, f.voices = append(f.asked, text), append(f.voices, voice)
	if f.fail != nil {
		return nil, f.fail
	}
	return []byte("3200"), nil // 0.1 s at 16 kHz
}

// fakeWirepod: say_text and trigger_wake_word on localhost.
type fakeWirepod struct {
	mu      sync.Mutex
	said    []string
	woken   int
	sdkInfo int // get_sdk_info calls (none when ip + guid are set by hand)
}

func (f *fakeWirepod) start(t *testing.T) *wirepod.Client {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/api-sdk/say_text":
			_ = r.ParseForm()
			f.said = append(f.said, r.Form.Get("text"))
			_, _ = w.Write([]byte("done"))
		case "/api-sdk/get_sdk_info":
			f.sdkInfo++
			http.Error(w, "unexpected", http.StatusInternalServerError)
		case "/api-sdk/trigger_wake_word":
			f.woken++
			_, _ = w.Write([]byte("done"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(hs.Close)
	return wirepod.New(hs.URL)
}

type fakeAsker struct{ question string }

func (f *fakeAsker) Ask(_ context.Context, q string, maxWords int) (ai.Answer, error) {
	f.question = q
	return ai.Answer{Text: "Pająk ma osiem nóg.", Words: 4, Model: "m"}, nil
}

type fakeWeather struct{ entity string }

func (f *fakeWeather) Weather(_ context.Context, entity string, days int) (hass.Weather, error) {
	f.entity = entity
	t := 21.0
	return hass.Weather{Entity: "weather.home", Condition: "sunny", ConditionPL: "słonecznie", Temperature: &t, Unit: "°C",
		Text: "Na dworze jest słonecznie, 21 stopni.", Forecast: make([]hass.Day, days)}, nil
}

func studioCtx() context.Context { return WithCaller(context.Background(), studio) }

func cmd(t *testing.T, m *Manager, ctx context.Context, name string, kv map[string]any) (any, error) {
	t.Helper()
	return m.Command(ctx, name, jsonArgs(t, kv))
}

// ---- voices ----

func TestSayVoicePiperAndEffect(t *testing.T) {
	tts := &fakeTTS{}
	var effects []string
	robot, _, m := startWith(t, func(c *Config) {
		c.TTS = tts
		c.Effect = func(_ context.Context, wav []byte, e string) ([]byte, error) {
			effects = append(effects, e)
			return []byte("6400"), nil
		}
	})
	id := begin(t, m, nil)
	res, err := cmd(t, m, studioCtx(), "say_voice", map[string]any{"session": id, "text": " Cześć,   jestem Vector! ", "voice": "pl_PL-gosia-medium", "effect": "mouse"})
	if err != nil {
		t.Fatal(err)
	}
	r := res.(map[string]any)
	if r["engine"] != "piper" || r["duration"] != 0.2 {
		t.Fatalf("result %v", r)
	}
	if tts.asked[0] != "Cześć, jestem Vector!" || tts.voices[0] != "pl_PL-gosia-medium" || effects[0] != "mouse" {
		t.Fatalf("asked %v %v %v", tts.asked, tts.voices, effects)
	}
	robot.get(func() {
		if len(robot.audio) != 1 || robot.audio[0].bytes != 6400 || !robot.audio[0].completed {
			t.Fatalf("audio %+v", robot.audio)
		}
	})
	// no effect: the effect function is not called; wait false queues and returns
	res, err = cmd(t, m, studioCtx(), "say_voice", map[string]any{"session": id, "text": "x", "wait": false})
	if err != nil || res.(map[string]any)["queued"] == nil || len(effects) != 1 {
		t.Fatalf("no wait: %v %v effects=%v", res, err, effects)
	}
	// the default voice is the client's (empty here)
	if tts.voices[1] != "" {
		t.Fatalf("voice %q", tts.voices[1])
	}
}

func TestSayVoiceFallbackToWirepod(t *testing.T) {
	wp := &fakeWirepod{}
	tts := &fakeTTS{fail: errors.New("connection refused")}
	_, _, m := startWith(t, func(c *Config) { c.TTS, c.Wirepod = tts, wp.start(t) })
	res, err := m.Command(context.Background(), "say_voice", jsonArgs(t, map[string]any{"text": "Dzień dobry"}))
	if err != nil {
		t.Fatal(err)
	}
	r := res.(map[string]any)
	if r["engine"] != "wirepod" || r["fallback"] != true || !strings.Contains(r["error"].(string), "refused") {
		t.Fatalf("result %v", r)
	}
	if wp.said[0] != "Dzień dobry" {
		t.Fatalf("said %v", wp.said)
	}
	// robot_ip + guid set by hand: wire-pod is never asked for them (only used to speak)
	wp.mu.Lock()
	if wp.sdkInfo != 0 {
		t.Fatalf("get_sdk_info called %d times", wp.sdkInfo)
	}
	wp.mu.Unlock()
	// no Piper configured at all: wire-pod speaks, no error field
	wp2 := &fakeWirepod{}
	_, _, m2 := startWith(t, func(c *Config) { c.Wirepod = wp2.start(t) })
	res, err = m2.Command(context.Background(), "say_voice", jsonArgs(t, map[string]any{"text": "a"}))
	if err != nil || res.(map[string]any)["error"] != nil || len(wp2.said) != 1 {
		t.Fatalf("%v %v", res, err)
	}
	if _, err := m2.Command(context.Background(), "list_voices", nil); err == nil || !strings.Contains(err.Error(), "piper_url") {
		t.Fatalf("list_voices without piper: %v", err)
	}
}

func TestSayVoiceValidation(t *testing.T) {
	_, _, m := startWith(t, func(c *Config) { c.TTS = &fakeTTS{} })
	for _, kv := range []map[string]any{
		{"text": ""},
		{"text": strings.Repeat("a", maxVoiceText+1)},
		{"text": "a", "voice": "../etc"},
		{"text": "a", "effect": "chipmunk"},
	} {
		if _, err := cmd(t, m, context.Background(), "say_voice", kv); err == nil {
			t.Errorf("%v accepted", kv)
		}
	}
	res, err := m.Command(studioCtx(), "list_voices", nil)
	if err != nil {
		t.Fatalf("list_voices is free for the studio: %v", err)
	}
	if v := res.(map[string]any)["voices"].([]wyoming.Voice); v[0].Name != "pl_PL-gosia-medium" {
		t.Fatalf("voices %v", v)
	}
	// the studio needs its session to speak
	if _, err := cmd(t, m, studioCtx(), "say_voice", map[string]any{"text": "a"}); !errors.Is(err, ErrSessionRequired) {
		t.Fatalf("studio without session: %v", err)
	}
}

func TestEffectFilter(t *testing.T) {
	for _, e := range Effects[1:] {
		f, err := effectFilter(e, 22050)
		if err != nil || f == "" {
			t.Errorf("%s: %q %v", e, f, err)
		}
	}
	if f, _ := effectFilter("mouse", 22050); !strings.HasPrefix(f, "asetrate=33075,aresample=22050") {
		t.Errorf("mouse %q", f)
	}
	if _, err := effectFilter("x", 16000); err == nil {
		t.Error("unknown effect accepted")
	}
	wav := wyoming.WAV(make([]byte, 10), wyoming.Format{Rate: 16000, Width: 2, Channels: 1})
	if r := wavRate(wav); r != 16000 {
		t.Errorf("rate %d", r)
	}
	if wavRate([]byte("nope")) != 0 {
		t.Error("rate of garbage")
	}
	// "none" needs no ffmpeg
	if out, err := FFmpegEffect(context.Background(), []byte("x"), "none"); err != nil || string(out) != "x" {
		t.Errorf("none: %q %v", out, err)
	}
}

func TestPlayWaitStopped(t *testing.T) {
	_, _, m := start(t)
	done := make(chan error, 1)
	go func() {
		_, err := m.Player.PlayWait(context.Background(), []byte("640000"), -1) // 20 s
		done <- err
	}()
	eventually(t, "playing", func() bool { return m.Snapshot().Audio.Playing })
	m.Player.Stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("PlayWait did not return after Stop")
	}
}

// ---- listening ----

func TestListenClaimsSpeech(t *testing.T) {
	var mu sync.Mutex
	var claims []bool
	_, sink, m := start(t)
	m.SetClaimHook(func(on bool) { mu.Lock(); claims = append(claims, on); mu.Unlock() })

	// without a program nothing is taken
	if m.TakeSpeech("00a1b2c3", "hej") {
		t.Fatal("taken without a program")
	}
	id := begin(t, m, map[string]any{"claim_speech": true, "heartbeat": 10})
	mu.Lock()
	if len(claims) != 1 || !claims[0] {
		t.Fatalf("claims %v", claims)
	}
	mu.Unlock()
	if m.TakeSpeech("ffffffff", "inny robot") {
		t.Fatal("another robot's sentence taken")
	}
	// heard without listen: noted for sensors only
	if !m.TakeSpeech("00a1b2c3", "pierwsze zdanie") {
		t.Fatal("not taken")
	}
	s := m.SensorsNow()
	if s.LastSpeech == nil || s.LastSpeech.Text != "pierwsze zdanie" || s.Counts.Speech != 1 {
		t.Fatalf("sensors %+v", s.LastSpeech)
	}
	if e := sink.last("program_heard"); e == nil || e.Data["chars"] != 15 || e.Data["text"] != nil {
		t.Fatalf("program_heard %+v", e)
	}

	// listen gets the next sentence
	type out struct {
		res any
		err error
	}
	got := make(chan out, 1)
	go func() {
		r, err := cmd(t, m, studioCtx(), "listen", map[string]any{"session": id, "timeout": 5})
		got <- out{r, err}
	}()
	eventually(t, "listening", func() bool { m.listenMu.Lock(); defer m.listenMu.Unlock(); return m.listener != nil })
	if _, err := cmd(t, m, studioCtx(), "listen", map[string]any{"session": id}); !errors.Is(err, ErrAlreadyListening) {
		t.Fatalf("second listen: %v", err)
	}
	m.TakeSpeech("", "zielony")
	o := <-got
	if o.err != nil {
		t.Fatal(o.err)
	}
	r := o.res.(map[string]any)
	if r["heard"] != true || r["text"] != "zielony" || r["woke"] != false {
		t.Fatalf("listen %v", r)
	}

	// nothing said: heard false after the timeout (2 s minimum)
	start := time.Now()
	res, err := cmd(t, m, studioCtx(), "listen", map[string]any{"session": id, "timeout": 0.1})
	if err != nil || res.(map[string]any)["heard"] != false || time.Since(start) < 2*time.Second {
		t.Fatalf("timeout listen %v %v after %s", res, err, time.Since(start))
	}

	// the end gives speech back to Home Assistant
	if _, err := cmd(t, m, studioCtx(), "program_end", map[string]any{"session": id}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(claims) != 2 || claims[1] {
		t.Fatalf("claims %v", claims)
	}
	mu.Unlock()
	if m.TakeSpeech("", "po programie") {
		t.Fatal("taken after the program")
	}
}

func TestListenNeedsClaim(t *testing.T) {
	_, _, m := start(t)
	// no voice router (no claim hook): claim_speech is refused quietly
	res, err := m.Command(studioCtx(), "program_begin", jsonArgs(t, map[string]any{"claim_speech": true}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.(map[string]any)["session"].(string)
	if res.(map[string]any)["claim_speech"] != false {
		t.Fatalf("claimed without a router: %v", res)
	}
	if _, err := cmd(t, m, studioCtx(), "listen", map[string]any{"session": id}); !errors.Is(err, ErrSpeechNotClaimed) {
		t.Fatalf("listen: %v", err)
	}
	if _, err := cmd(t, m, WithCaller(context.Background(), ha), "listen", nil); !errors.Is(err, ErrSessionRequired) {
		t.Fatalf("listen from HA: %v", err)
	}
}

func TestListenPromptAndWake(t *testing.T) {
	wp := &fakeWirepod{}
	tts := &fakeTTS{}
	_, _, m := startWith(t, func(c *Config) { c.TTS, c.Wirepod = tts, wp.start(t) })
	m.SetClaimHook(func(bool) {})
	id := begin(t, m, map[string]any{"claim_speech": true, "heartbeat": 10})
	got := make(chan any, 1)
	go func() {
		r, _ := cmd(t, m, studioCtx(), "listen", map[string]any{"session": id, "timeout": 3, "prompt": "Powiedz kolor", "wake": true})
		got <- r
	}()
	eventually(t, "wake word", func() bool { wp.mu.Lock(); defer wp.mu.Unlock(); return wp.woken == 1 })
	m.TakeSpeech("", "czerwony")
	r := (<-got).(map[string]any)
	if r["text"] != "czerwony" || r["woke"] != true || tts.asked[0] != "Powiedz kolor" {
		t.Fatalf("%v asked %v", r, tts.asked)
	}
}

func TestListenEndsWithProgram(t *testing.T) {
	_, _, m := start(t)
	m.SetClaimHook(func(bool) {})
	id := begin(t, m, map[string]any{"claim_speech": true, "heartbeat": 10})
	got := make(chan error, 1)
	go func() {
		_, err := cmd(t, m, studioCtx(), "listen", map[string]any{"session": id, "timeout": 20})
		got <- err
	}()
	eventually(t, "listening", func() bool { m.listenMu.Lock(); defer m.listenMu.Unlock(); return m.listener != nil })
	if _, err := m.Command(WithCaller(context.Background(), ha), "stop", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-got:
		if err == nil {
			t.Fatal("listen went on after the program")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("listen did not end")
	}
}

// ---- behaviors ----

func shortBehavior(t *testing.T) {
	t.Helper()
	poll, startBy, settle, quiet, letGo := behaviorPoll, behaviorStartBy, behaviorSettle, behaviorQuietEnd, behaviorLetGo
	behaviorPoll, behaviorStartBy, behaviorSettle, behaviorQuietEnd, behaviorLetGo = 20*time.Millisecond, 500*time.Millisecond, 200*time.Millisecond, 600*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() {
		behaviorPoll, behaviorStartBy, behaviorSettle, behaviorQuietEnd, behaviorLetGo = poll, startBy, settle, quiet, letGo
	})
}

func TestRunBehaviorSuspendsControl(t *testing.T) {
	shortTimes(t)
	shortBehavior(t)
	robot, sink, m := start(t)
	robot.get(func() { robot.behave = true })
	id := begin(t, m, map[string]any{"heartbeat": 0.3})
	// keep the program alive; the behavior itself pauses the watchdog
	got := make(chan any, 1)
	go func() {
		r, err := cmd(t, m, studioCtx(), "run_behavior", map[string]any{"session": id, "behavior": "dance"})
		if err != nil {
			got <- err
			return
		}
		got <- r
	}()
	eventually(t, "behavior running", func() bool {
		m.progMu.Lock()
		defer m.progMu.Unlock()
		return m.prog != nil && m.prog.behavior == "dance"
	})
	if _, err := cmd(t, m, studioCtx(), "set_head_angle", map[string]any{"session": id, "angle": 0}); !errors.Is(err, ErrBehaviorRunning) {
		t.Fatalf("motion during the behavior: %v", err)
	}
	if _, err := cmd(t, m, WithCaller(context.Background(), ha), "set_head_angle", map[string]any{"angle": 0}); !errors.Is(err, ErrProgramRunning) {
		t.Fatalf("HA during the behavior: %v", err)
	}
	var r map[string]any
	select {
	case v := <-got:
		var ok bool
		if r, ok = v.(map[string]any); !ok {
			t.Fatalf("run_behavior: %v", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("behavior never ended")
	}
	if r["finished"] != true || r["reason"] != "finished" || r["control"] != true {
		t.Fatalf("result %v", r)
	}
	robot.get(func() {
		if len(robot.intents) != 1 || robot.intents[0] != "intent_imperative_dance" || robot.holdingAtIntent[0] {
			t.Fatalf("intents %v holding %v", robot.intents, robot.holdingAtIntent)
		}
	})
	if sink.has("program_ended") {
		t.Fatalf("the behavior ended the program: %+v", sink.last("program_ended"))
	}
	eventually(t, "control back", func() bool { return m.Snapshot().Control == "held" })
	// the watchdog runs again after the behavior
	eventually(t, "watchdog", func() bool { return sink.has("program_ended") })
	if e := sink.last("program_ended"); e.Data["reason"] != "watchdog" {
		t.Fatalf("ended %+v", e)
	}
}

func TestRunBehaviorNotStartedAndTimeout(t *testing.T) {
	shortBehavior(t)
	robot, _, m := start(t)
	id := begin(t, m, map[string]any{"heartbeat": 10})
	res, err := cmd(t, m, studioCtx(), "run_behavior", map[string]any{"session": id, "behavior": "look_at_me"})
	if err != nil {
		t.Fatal(err)
	}
	if r := res.(map[string]any); r["finished"] != false || r["reason"] != "not_started" {
		t.Fatalf("%v", r)
	}
	// the robot keeps busy: the timeout (minimum 5 s) ends the wait
	robot.get(func() { robot.behave = true })
	go func() {
		for i := 0; i < 400; i++ {
			robot.events <- &vectorpb.Event{EventType: &vectorpb.Event_RobotState{RobotState: &vectorpb.RobotState{Status: uint32(vectorpb.RobotStatus_ROBOT_STATUS_IS_ANIMATING)}}}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	res, err = cmd(t, m, studioCtx(), "run_behavior", map[string]any{"session": id, "behavior": "explore", "timeout": 1})
	if err != nil {
		t.Fatal(err)
	}
	if r := res.(map[string]any); r["reason"] != "timeout" || r["seconds"].(float64) < 4.5 {
		t.Fatalf("%v", r)
	}
}

func TestRunBehaviorStopped(t *testing.T) {
	shortBehavior(t)
	behaviorStartBy = 10 * time.Second
	robot, sink, m := start(t)
	id := begin(t, m, map[string]any{"heartbeat": 10})
	got := make(chan error, 1)
	go func() {
		_, err := cmd(t, m, studioCtx(), "run_behavior", map[string]any{"session": id, "behavior": "find_cube"})
		got <- err
	}()
	eventually(t, "intent sent", func() bool { var n int; robot.get(func() { n = len(robot.intents) }); return n == 1 })
	if _, err := cmd(t, m, studioCtx(), "stop", map[string]any{"session": id}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-got:
		if err == nil || err.Error() != "stopped" {
			t.Fatalf("err %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stop did not end the behavior")
	}
	if e := sink.last("program_ended"); e == nil || e.Data["reason"] != "stop" {
		t.Fatalf("ended %+v", e)
	}
}

func TestGoToChargerEndsProgram(t *testing.T) {
	shortBehavior(t)
	robot, sink, m := start(t)
	id := begin(t, m, nil)
	res, err := cmd(t, m, studioCtx(), "run_behavior", map[string]any{"session": id, "behavior": "go_to_charger"})
	if err != nil {
		t.Fatal(err)
	}
	if r := res.(map[string]any); r["program_ended"] != true {
		t.Fatalf("%v", r)
	}
	if e := sink.last("program_ended"); e == nil || e.Data["reason"] != "charger" {
		t.Fatalf("ended %+v", e)
	}
	robot.get(func() {
		if robot.intents[0] != "intent_system_charger" || robot.holdingAtIntent[0] {
			t.Fatalf("intents %v holding %v", robot.intents, robot.holdingAtIntent)
		}
	})
	if m.Program() != nil {
		t.Fatal("program still running")
	}
}

func TestRunBehaviorChecks(t *testing.T) {
	_, _, m := start(t)
	if _, err := cmd(t, m, WithCaller(context.Background(), ha), "run_behavior", map[string]any{"behavior": "dance"}); !errors.Is(err, ErrSessionRequired) {
		t.Fatalf("without session: %v", err)
	}
	id := begin(t, m, nil)
	if _, err := cmd(t, m, studioCtx(), "run_behavior", map[string]any{"session": id, "behavior": "wheelie"}); err == nil || !strings.Contains(err.Error(), "dance") {
		t.Fatalf("unknown behavior: %v", err)
	}
}

func TestBehaviorWatch(t *testing.T) {
	t0 := time.Unix(1000, 0)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	w := newBehaviorWatch(t0, behaviorObs{activitySeq: 3})
	if done, _ := w.step(at(100), behaviorObs{activitySeq: 3}); done {
		t.Fatal("done before start")
	}
	w.step(at(200), behaviorObs{activitySeq: 4, busy: true}) // began
	if done, _ := w.step(at(1000), behaviorObs{activitySeq: 4, busy: true}); done {
		t.Fatal("done while busy")
	}
	// activity changed again and calm for the settle time
	if done, why := w.step(at(1000+int(behaviorSettle/time.Millisecond)), behaviorObs{activitySeq: 5}); !done || why != "finished" {
		t.Fatalf("not finished: %v %s", done, why)
	}
	w2 := newBehaviorWatch(t0, behaviorObs{})
	if done, why := w2.step(at(int(behaviorStartBy/time.Millisecond)), behaviorObs{}); !done || why != "not_started" {
		t.Fatalf("not_started: %v %s", done, why)
	}
	w3 := newBehaviorWatch(t0, behaviorObs{})
	w3.step(at(100), behaviorObs{busy: true})
	if done, why := w3.step(at(100+int(behaviorQuietEnd/time.Millisecond)), behaviorObs{}); !done || why != "finished" {
		t.Fatalf("quiet end: %v %s", done, why)
	}
}

// ---- AI, weather, volume, mirror, photo, sensors ----

func TestAskAIAndWeather(t *testing.T) {
	_, _, off := start(t)
	if _, err := off.Command(context.Background(), "ask_ai", jsonArgs(t, map[string]any{"question": "x"})); !errors.Is(err, ai.ErrOff) {
		t.Fatalf("ai off: %v", err)
	}
	if _, err := off.Command(context.Background(), "weather", nil); !errors.Is(err, hass.ErrNoHA) {
		t.Fatalf("weather off: %v", err)
	}
	asker, weather := &fakeAsker{}, &fakeWeather{}
	_, _, m := startWith(t, func(c *Config) { c.AI, c.Weather = asker, weather })
	if _, err := cmd(t, m, studioCtx(), "ask_ai", map[string]any{"question": "x"}); !errors.Is(err, ErrSessionRequired) {
		t.Fatalf("ask_ai without session: %v", err)
	}
	id := begin(t, m, nil)
	res, err := cmd(t, m, studioCtx(), "ask_ai", map[string]any{"session": id, "question": "ile nóg ma pająk?", "max_words": 20})
	if err != nil || res.(ai.Answer).Text != "Pająk ma osiem nóg." || asker.question != "ile nóg ma pająk?" {
		t.Fatalf("%v %v", res, err)
	}
	// weather is free for the studio (reading)
	res, err = cmd(t, m, WithCaller(context.Background(), Caller{ID: "ws-x", Studio: true}), "weather", map[string]any{"forecast_days": 9})
	if err != nil {
		t.Fatal(err)
	}
	if w := res.(hass.Weather); w.ConditionPL != "słonecznie" || len(w.Forecast) != 5 {
		t.Fatalf("weather %+v", w)
	}
}

func TestSetVolumeRestoredAtEnd(t *testing.T) {
	_, _, m := start(t)
	var mu sync.Mutex
	var set []int
	m.setVolume = func(_ context.Context, level int) error { mu.Lock(); set = append(set, level); mu.Unlock(); return nil }
	eventually(t, "settings read", func() bool { return m.volumeNow() == 2 })
	id := begin(t, m, nil)
	for _, l := range []int{5, 9} {
		if _, err := cmd(t, m, studioCtx(), "set_volume", map[string]any{"session": id, "level": l}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cmd(t, m, studioCtx(), "program_end", map[string]any{"session": id}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "volume restored", func() bool { mu.Lock(); defer mu.Unlock(); return len(set) == 3 })
	mu.Lock()
	defer mu.Unlock()
	if set[0] != 5 || set[1] != 5 || set[2] != 2 {
		t.Fatalf("set %v", set)
	}
}

func TestMirrorModeOffAtEnd(t *testing.T) {
	robot, _, m := start(t)
	id := begin(t, m, nil)
	if _, err := cmd(t, m, studioCtx(), "mirror_mode", map[string]any{"session": id, "enable": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Command(WithCaller(context.Background(), ha), "stop", nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "mirror off", func() bool {
		var n []bool
		robot.get(func() { n = append(n, robot.mirror...) })
		return len(n) == 2 && n[0] && !n[1]
	})
}

func TestParentCommands(t *testing.T) {
	_, _, m := start(t)
	parent := WithCaller(context.Background(), Caller{ID: "ws-parent", Studio: true, Parent: true})
	for _, name := range []string{"take_photo", "enroll_face"} {
		if _, err := cmd(t, m, studioCtx(), name, map[string]any{"name": "Alice"}); !errors.Is(err, ErrParentOnly) {
			t.Fatalf("%s with the child's token: %v", name, err)
		}
	}
	if _, err := cmd(t, m, parent, "take_photo", nil); !errors.Is(err, ErrSessionRequired) {
		t.Fatalf("parent without session: %v", err)
	}
	res, err := m.Command(parent, "program_begin", Args{})
	if err != nil {
		t.Fatal(err)
	}
	id := res.(map[string]any)["session"].(string)
	res, err = cmd(t, m, parent, "take_photo", map[string]any{"session": id})
	if err != nil {
		t.Fatal(err)
	}
	if img := res.(map[string]any)["image"].(string); !strings.HasPrefix(img, "data:image/jpeg;base64,/9") {
		t.Fatalf("image %q", img)
	}
	// the integration's token may take photos without a program
	if _, err := cmd(t, m, WithCaller(context.Background(), Caller{ID: "ws-other"}), "take_photo", nil); !errors.Is(err, ErrProgramRunning) {
		t.Fatalf("HA during a program: %v", err)
	}
}

func TestSensorsStimulationVolumeLife(t *testing.T) {
	_, _, m := start(t)
	eventually(t, "settings read", func() bool { return m.volumeNow() == 2 })
	m.update(func(tr *Tracker) bool {
		tr.S.Stimulation = 0.7
		tr.S.Stats = map[string]any{"Alive.seconds": 864000.0, "Pet.ms": 600000.0, "Odom.Body": 826811017879.0, "BStat.ReactedToTriggerWord": 412.0}
		return true
	})
	s := m.SensorsNow()
	if s.Stimulation != 0.7 || s.Volume == nil || *s.Volume != 2 {
		t.Fatalf("stimulation %v volume %v", s.Stimulation, s.Volume)
	}
	l := s.Life
	if *l.AliveDays != 10 || *l.PetMinutes != 10 || *l.DistanceM != 826.8 || *l.WakeWords != 412 {
		t.Fatalf("life %+v", l)
	}
	m.update(func(tr *Tracker) bool { tr.S.Stats = map[string]any{}; return true })
	if m.SensorsNow().Life.AliveDays != nil {
		t.Fatal("missing stat not nil")
	}
}

func TestActivitySeq(t *testing.T) {
	tr := NewTracker("e", "n", "")
	st := func(name string) *vectorpb.Event {
		return &vectorpb.Event{EventType: &vectorpb.Event_TimeStampedStatus{TimeStampedStatus: &vectorpb.TimeStampedStatus{
			Status: &vectorpb.Status{StatusType: &vectorpb.Status_FeatureStatus{FeatureStatus: &vectorpb.FeatureStatus{FeatureName: name}}}}}}
	}
	now := time.Now()
	for _, n := range []string{"A", "A", "NoFeature", "B"} {
		tr.ApplyEvent(st(n), now)
	}
	if tr.live.activitySeq != 2 {
		t.Fatalf("seq %d", tr.live.activitySeq)
	}
}

func TestCameraLeavesControlDuringBehavior(t *testing.T) {
	cameraLinger, noFrameWait = 100*time.Millisecond, 200*time.Millisecond
	shortBehavior(t)
	behaviorStartBy = 3 * time.Second
	robot, _, m := start(t)
	robot.get(func() { robot.needsControl = true }) // like on the charger
	id := begin(t, m, map[string]any{"heartbeat": 10})
	got := make(chan error, 1)
	go func() {
		_, err := cmd(t, m, studioCtx(), "run_behavior", map[string]any{"session": id, "behavior": "look_at_me"})
		got <- err
	}()
	eventually(t, "behavior running", m.behaviorRunning)
	eventually(t, "control let go", func() (ok bool) { robot.get(func() { ok = !robot.holding }); return ok })
	var controls int
	robot.get(func() { controls = robot.controls })
	// somebody watches the camera (the studio's view) while the behavior runs
	frames, stop := m.Camera.Watch()
	select {
	case <-frames:
		t.Fatal("a frame without control on the charger")
	case <-time.After(time.Second):
	}
	robot.get(func() {
		if robot.controls != controls || robot.holding {
			t.Fatalf("the camera took control during the behavior (controls %d → %d)", controls, robot.controls)
		}
	})
	// after the behavior the program holds control again and the view comes back
	if err := <-got; err != nil {
		t.Fatal(err)
	}
	select {
	case <-frames:
	case <-time.After(3 * time.Second):
		t.Fatal("no frame after the behavior")
	}
	stop()
}

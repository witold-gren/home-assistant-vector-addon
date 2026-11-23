package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/robot"
)

// programRobot records callers, speech and departed connections.
type programRobot struct {
	mu      sync.Mutex
	callers []robot.Caller
	gone    []string
	speech  []string
	program *robot.ProgramInfo
	audioOK bool
	played  int
}

func (p *programRobot) ESN() string { return "00a1b2c3" }
func (p *programRobot) Snapshot() robot.State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return robot.State{ESN: "00a1b2c3", Connected: true, Program: p.program}
}
func (p *programRobot) Command(ctx context.Context, name string, _ robot.Args) (any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.callers = append(p.callers, robot.CallerOf(ctx))
	return name, nil
}
func (p *programRobot) Program() *robot.ProgramInfo { return p.Snapshot().Program }
func (p *programRobot) CallerGone(id string) {
	p.mu.Lock()
	p.gone = append(p.gone, id)
	p.mu.Unlock()
}
func (p *programRobot) NoteSpeech(text string) {
	p.mu.Lock()
	p.speech = append(p.speech, text)
	p.mu.Unlock()
}
func (p *programRobot) GuardAudio(c robot.Caller, session string) error {
	if c.Studio && session == "" || !c.Studio && !p.audioOK {
		return robot.ErrProgramRunning
	}
	return nil
}
func (p *programRobot) PlayAudio([]byte, int, bool) (int, error) {
	p.mu.Lock()
	p.played++
	p.mu.Unlock()
	return 0, nil
}

func programSetup(t *testing.T) (*Server, *programRobot, *httptest.Server) {
	srv := New("test", "secret", slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.StudioToken = "kids"
	rb := &programRobot{audioOK: true}
	srv.AddRobot(rb)
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return srv, rb, hs
}

func command(t *testing.T, c *websocket.Conn, s string) map[string]any {
	t.Helper()
	if err := c.Write(context.Background(), websocket.MessageText, []byte(s)); err != nil {
		t.Fatal(err)
	}
	for {
		m := read(t, c)
		if m["type"] == "result" {
			return m
		}
	}
}

func TestStudioToken(t *testing.T) {
	_, rb, hs := programSetup(t)
	c, err := dial(t, hs, "kids")
	if err != nil {
		t.Fatal(err)
	}
	hello := read(t, c)
	if hello["role"] != "studio" {
		t.Fatalf("hello role: %v", hello)
	}
	if f, _ := hello["features"].([]any); len(f) != 1 || f[0] != "program" {
		t.Fatalf("features: %v", hello["features"])
	}
	if _, ok := hello["program"]; !ok || hello["program"] != nil {
		t.Fatalf("hello program: %v", hello["program"])
	}
	for _, cmd := range []string{"program_begin", "sensors", "drive_straight", "say_text", "set_wheels", "stop"} {
		if r := command(t, c, `{"id":1,"command":"`+cmd+`"}`); r["success"] != true {
			t.Fatalf("studio %s: %v", cmd, r)
		}
	}
	for _, cmd := range []string{"update_settings", "delete_face", "rename_face", "enroll_face", "delete_photo", "list_photos",
		"camera_snapshot", "hold_control", "release_control", "move", "app_intent", "warm_stream", "set_voice_router", "forget_cube"} {
		if r := command(t, c, `{"id":2,"command":"`+cmd+`"}`); r["success"] != false || r["error"] != "not allowed with the studio token" {
			t.Fatalf("studio %s: %v", cmd, r)
		}
	}
	rb.mu.Lock()
	last := rb.callers[len(rb.callers)-1]
	rb.mu.Unlock()
	if !last.Studio || !strings.HasPrefix(last.ID, "ws-") {
		t.Fatalf("caller %+v", last)
	}
	// the studio token does not open the camera or the microphone
	for _, path := range []string{"/snapshot?token=kids", "/mjpeg?token=kids", "/mic?token=kids"} {
		resp, err := http.Get(hs.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
	}
	// the connection closes: the robot hears about it (its program stops)
	_ = c.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(2 * time.Second)
	for {
		rb.mu.Lock()
		n := len(rb.gone)
		rb.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("CallerGone not called")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNoStudioTokenRefusesStudio(t *testing.T) {
	srv, _, hs := programSetup(t)
	srv.StudioToken = ""
	if _, err := dial(t, hs, ""); err == nil {
		t.Fatal("empty token accepted")
	}
	if _, err := dial(t, hs, "kids"); err == nil {
		t.Fatal("studio accepted without a studio token")
	}
	c, err := dial(t, hs, "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if hello := read(t, c); hello["role"] != nil {
		t.Fatalf("integration hello role: %v", hello["role"])
	}
	if r := command(t, c, `{"id":1,"command":"update_settings"}`); r["success"] != true {
		t.Fatalf("integration: %v", r)
	}
}

func TestHelloProgramAndSpeech(t *testing.T) {
	srv, rb, hs := programSetup(t)
	rb.program = &robot.ProgramInfo{Owner: "rcs", Since: 100, Limit: 300}
	c, err := dial(t, hs, "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	hello := read(t, c)
	if p, _ := hello["program"].(map[string]any); p["owner"] != "rcs" || p["limit"] != float64(300) {
		t.Fatalf("hello program: %v", hello["program"])
	}
	srv.Event("00a1b2c3", robot.Event{Name: "speech", Data: map[string]any{"id": 1, "text": "dance"}})
	if e := read(t, c); e["event"] != "speech" {
		t.Fatalf("speech event: %v", e)
	}
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if len(rb.speech) != 1 || rb.speech[0] != "dance" {
		t.Fatalf("speech noted: %v", rb.speech)
	}
}

func TestAudioDuringProgram(t *testing.T) {
	_, rb, hs := programSetup(t)
	post := func(q string) int {
		resp, err := http.Post(hs.URL+"/audio?"+q, "audio/wav", strings.NewReader("RIFF"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post("token=secret"); code != http.StatusAccepted {
		t.Fatalf("integration audio: %d", code)
	}
	rb.audioOK = false // a program runs
	if code := post("token=secret"); code != http.StatusConflict {
		t.Fatalf("integration audio during a program: %d", code)
	}
	if code := post("token=kids"); code != http.StatusConflict {
		t.Fatalf("studio audio without a session: %d", code)
	}
	if code := post("token=kids&session=abc"); code != http.StatusAccepted {
		t.Fatalf("studio audio in its session: %d", code)
	}
	if code := post("token=wrong"); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", code)
	}
}

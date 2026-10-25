package robot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// fakeRobot implements the RPCs the manager uses.
type fakeRobot struct {
	vectorpb.UnimplementedExternalInterfaceServer

	mu        sync.Mutex
	events    chan *vectorpb.Event
	endStream chan struct{}
	controls  int // ControlRequests seen
	releases  int // ControlReleases seen
	loseCtl   chan struct{}
	heads     []float32
	wheels    []float32
	stops     int
	streams   int
	// camera: frames only while behavior control is held (like on the charger) when needsControl
	needsControl bool
	blockFrames  bool // camera sends nothing at all
	holding      bool
	feeds        int
	photos       []*vectorpb.PhotoInfo
	// audio: one entry per stream
	audio []audioStream
	faces map[int32]string
	goTo  [][3]float32
}

type audioStream struct {
	volume    uint32
	bytes     int
	completed bool
	cancelled bool
}

func newFakeRobot() *fakeRobot {
	return &fakeRobot{events: make(chan *vectorpb.Event, 16), endStream: make(chan struct{}, 1), loseCtl: make(chan struct{}, 1)}
}

func (f *fakeRobot) BatteryState(context.Context, *vectorpb.BatteryStateRequest) (*vectorpb.BatteryStateResponse, error) {
	return &vectorpb.BatteryStateResponse{BatteryLevel: vectorpb.BatteryLevel_BATTERY_LEVEL_NOMINAL, BatteryVolts: 3.9, IsOnChargerPlatform: true}, nil
}

func (f *fakeRobot) VersionState(context.Context, *vectorpb.VersionStateRequest) (*vectorpb.VersionStateResponse, error) {
	return &vectorpb.VersionStateResponse{OsVersion: "2.0.1.6076ep"}, nil
}

func (f *fakeRobot) PullJdocs(context.Context, *vectorpb.PullJdocsRequest) (*vectorpb.PullJdocsResponse, error) {
	return &vectorpb.PullJdocsResponse{NamedJdocs: []*vectorpb.NamedJdoc{{JdocType: vectorpb.JdocType_ROBOT_SETTINGS, Doc: &vectorpb.Jdoc{JsonDoc: `{"master_volume":2,"eye_color":3}`}}}}, nil
}

func (f *fakeRobot) EventStream(_ *vectorpb.EventRequest, s vectorpb.ExternalInterface_EventStreamServer) error {
	f.mu.Lock()
	f.streams++
	f.mu.Unlock()
	keep := time.NewTicker(50 * time.Millisecond)
	defer keep.Stop()
	for {
		select {
		case <-s.Context().Done():
			return nil
		case <-f.endStream:
			return errors.New("robot went away")
		case ev := <-f.events:
			if err := s.Send(&vectorpb.EventResponse{Event: ev}); err != nil {
				return err
			}
		case <-keep.C:
			_ = s.Send(&vectorpb.EventResponse{Event: &vectorpb.Event{EventType: &vectorpb.Event_KeepAlive{KeepAlive: &vectorpb.KeepAlivePing{}}}})
		}
	}
}

func (f *fakeRobot) BehaviorControl(s vectorpb.ExternalInterface_BehaviorControlServer) error {
	recv := make(chan *vectorpb.BehaviorControlRequest)
	go func() {
		for {
			r, err := s.Recv()
			if err != nil {
				close(recv)
				return
			}
			recv <- r
		}
	}()
	for {
		select {
		case r, ok := <-recv:
			if !ok {
				return nil
			}
			f.mu.Lock()
			if r.GetControlRequest() != nil {
				f.controls++
				f.holding = true
			} else if r.GetControlRelease() != nil {
				f.releases++
				f.holding = false
			}
			f.mu.Unlock()
			if r.GetControlRequest() != nil {
				_ = s.Send(&vectorpb.BehaviorControlResponse{ResponseType: &vectorpb.BehaviorControlResponse_ControlGrantedResponse{ControlGrantedResponse: &vectorpb.ControlGrantedResponse{}}})
			}
		case <-f.loseCtl:
			_ = s.Send(&vectorpb.BehaviorControlResponse{ResponseType: &vectorpb.BehaviorControlResponse_ControlLostEvent{ControlLostEvent: &vectorpb.ControlLostResponse{}}})
		case <-s.Context().Done():
			f.mu.Lock()
			f.holding = false
			f.mu.Unlock()
			return nil
		}
	}
}

func (f *fakeRobot) CameraFeed(_ *vectorpb.CameraFeedRequest, s vectorpb.ExternalInterface_CameraFeedServer) error {
	f.mu.Lock()
	f.feeds++
	f.mu.Unlock()
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for n := 0; ; n++ {
		select {
		case <-s.Context().Done():
			return nil
		case <-t.C:
			f.mu.Lock()
			send := (!f.needsControl || f.holding) && !f.blockFrames
			f.mu.Unlock()
			if send {
				if err := s.Send(&vectorpb.CameraFeedResponse{ImageEncoding: vectorpb.ImageChunk_JPEG_COLOR, Data: []byte{0xff, 0xd8, byte(n)}}); err != nil {
					return err
				}
			}
		}
	}
}

func (f *fakeRobot) ExternalAudioStreamPlayback(s vectorpb.ExternalInterface_ExternalAudioStreamPlaybackServer) error {
	f.mu.Lock()
	f.audio = append(f.audio, audioStream{})
	i := len(f.audio) - 1
	f.mu.Unlock()
	for {
		r, err := s.Recv()
		if err != nil {
			return nil
		}
		f.mu.Lock()
		switch {
		case r.GetAudioStreamPrepare() != nil:
			f.audio[i].volume = r.GetAudioStreamPrepare().GetAudioVolume()
		case r.GetAudioStreamChunk() != nil:
			f.audio[i].bytes += len(r.GetAudioStreamChunk().GetAudioChunkSamples())
		case r.GetAudioStreamCancel() != nil:
			f.audio[i].cancelled = true
		case r.GetAudioStreamComplete() != nil:
			f.audio[i].completed = true
		}
		done := f.audio[i].completed
		f.mu.Unlock()
		if done {
			return s.Send(&vectorpb.ExternalAudioStreamResponse{AudioResponseType: &vectorpb.ExternalAudioStreamResponse_AudioStreamPlaybackComplete{
				AudioStreamPlaybackComplete: &vectorpb.ExternalAudioStreamPlaybackComplete{}}})
		}
	}
}

func (f *fakeRobot) RequestEnrolledNames(context.Context, *vectorpb.RequestEnrolledNamesRequest) (*vectorpb.RequestEnrolledNamesResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*vectorpb.LoadedKnownFace
	for id, name := range f.faces {
		out = append(out, &vectorpb.LoadedKnownFace{FaceId: id, Name: name})
	}
	return &vectorpb.RequestEnrolledNamesResponse{Faces: out}, nil
}

func (f *fakeRobot) UpdateEnrolledFaceByID(_ context.Context, r *vectorpb.UpdateEnrolledFaceByIDRequest) (*vectorpb.UpdateEnrolledFaceByIDResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.faces[r.GetFaceId()] == r.GetOldName() {
		f.faces[r.GetFaceId()] = r.GetNewName()
	}
	return &vectorpb.UpdateEnrolledFaceByIDResponse{}, nil
}

func (f *fakeRobot) GoToPose(_ context.Context, r *vectorpb.GoToPoseRequest) (*vectorpb.GoToPoseResponse, error) {
	f.mu.Lock()
	f.goTo = append(f.goTo, [3]float32{r.GetXMm(), r.GetYMm(), r.GetRad()})
	f.mu.Unlock()
	return &vectorpb.GoToPoseResponse{Result: &vectorpb.ActionResult{Code: vectorpb.ActionResult_ACTION_RESULT_SUCCESS}}, nil
}

func (f *fakeRobot) PhotosInfo(context.Context, *vectorpb.PhotosInfoRequest) (*vectorpb.PhotosInfoResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &vectorpb.PhotosInfoResponse{PhotoInfos: f.photos}, nil
}

func (f *fakeRobot) Photo(_ context.Context, r *vectorpb.PhotoRequest) (*vectorpb.PhotoResponse, error) {
	for _, p := range f.photos {
		if p.GetPhotoId() == r.GetPhotoId() {
			return &vectorpb.PhotoResponse{Success: true, Image: []byte("jpeg")}, nil
		}
	}
	return &vectorpb.PhotoResponse{Success: false}, nil
}

func (f *fakeRobot) SetHeadAngle(_ context.Context, r *vectorpb.SetHeadAngleRequest) (*vectorpb.SetHeadAngleResponse, error) {
	f.mu.Lock()
	f.heads = append(f.heads, r.GetAngleRad())
	f.mu.Unlock()
	if r.GetIdTag() < sdkTagFirst {
		return &vectorpb.SetHeadAngleResponse{Result: &vectorpb.ActionResult{Code: vectorpb.ActionResult_ACTION_RESULT_CANCELLED_WHILE_RUNNING}}, nil
	}
	return &vectorpb.SetHeadAngleResponse{Result: &vectorpb.ActionResult{Code: vectorpb.ActionResult_ACTION_RESULT_SUCCESS}}, nil
}

func (f *fakeRobot) DriveWheels(_ context.Context, r *vectorpb.DriveWheelsRequest) (*vectorpb.DriveWheelsResponse, error) {
	f.mu.Lock()
	f.wheels = append(f.wheels, r.GetLeftWheelMmps())
	f.mu.Unlock()
	return &vectorpb.DriveWheelsResponse{}, nil
}

func (f *fakeRobot) MoveHead(_ context.Context, r *vectorpb.MoveHeadRequest) (*vectorpb.MoveHeadResponse, error) {
	f.mu.Lock()
	f.heads = append(f.heads, r.GetSpeedRadPerSec())
	f.mu.Unlock()
	return &vectorpb.MoveHeadResponse{}, nil
}

func (f *fakeRobot) MoveLift(context.Context, *vectorpb.MoveLiftRequest) (*vectorpb.MoveLiftResponse, error) {
	return &vectorpb.MoveLiftResponse{}, nil
}

func (f *fakeRobot) StopAllMotors(context.Context, *vectorpb.StopAllMotorsRequest) (*vectorpb.StopAllMotorsResponse, error) {
	f.mu.Lock()
	f.stops++
	f.mu.Unlock()
	return &vectorpb.StopAllMotorsResponse{}, nil
}

func (f *fakeRobot) get(fn func()) { f.mu.Lock(); defer f.mu.Unlock(); fn() }

type fakeSink struct {
	mu     sync.Mutex
	states []State
	events []Event
}

func (s *fakeSink) State(st State) { s.mu.Lock(); s.states = append(s.states, st); s.mu.Unlock() }
func (s *fakeSink) Event(_ string, e Event) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
}
func (s *fakeSink) has(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.events {
		if e.Name == name {
			return true
		}
	}
	return false
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func start(t *testing.T) (*fakeRobot, *fakeSink, *Manager) {
	t.Helper()
	backoff = []time.Duration{20 * time.Millisecond}
	robot := newFakeRobot()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	vectorpb.RegisterExternalInterfaceServer(srv, robot)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	dial := func(ip, guid string) (vectorpb.ExternalInterfaceClient, func(), error) {
		conn, err := grpc.NewClient("passthrough:///bufnet",
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return nil, nil, err
		}
		return vectorpb.NewExternalInterfaceClient(conn), func() { _ = conn.Close() }, nil
	}
	sink := &fakeSink{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// fake decoder: the "file" says how many PCM bytes it decodes to
	decode := func(_ context.Context, data []byte) ([]byte, error) {
		n, err := strconv.Atoi(string(data))
		return make([]byte, n), err
	}
	m := NewManager(Config{ESN: "00a1b2c3", IP: "10.0.0.1", GUID: "guid", ReleaseAfter: 100 * time.Millisecond, Decode: decode}, log, sink, dial)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go m.Run(ctx)
	eventually(t, "connection", func() bool { return m.Snapshot().Connected })
	return robot, sink, m
}

func TestConnectStateAndEvents(t *testing.T) {
	robot, sink, m := start(t)
	eventually(t, "firmware and settings", func() bool {
		s := m.Snapshot()
		return s.Firmware == "2.0.1.6076ep" && s.Settings["master_volume"] == float64(2)
	})
	if b := m.Snapshot().Battery; b == nil || b.Percent != 60 || !b.OnCharger {
		t.Fatalf("battery %+v", b)
	}
	robot.events <- &vectorpb.Event{EventType: &vectorpb.Event_RobotObservedFace{RobotObservedFace: &vectorpb.RobotObservedFace{FaceId: 3, Name: "Alice"}}}
	eventually(t, "face_seen", func() bool { return sink.has("face_seen") })
	eventually(t, "state push", func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		return len(sink.states) > 0 && sink.states[len(sink.states)-1].LastFace != nil
	})
}

func TestActionTakesAndReleasesControl(t *testing.T) {
	robot, _, m := start(t)
	if _, err := m.Command(context.Background(), "set_head_angle", Args{"angle": []byte("30")}); err != nil {
		t.Fatal(err)
	}
	robot.get(func() {
		if robot.controls != 1 || len(robot.heads) != 1 {
			t.Fatalf("controls=%d heads=%v", robot.controls, robot.heads)
		}
	})
	if m.Snapshot().Control != "auto" {
		t.Fatalf("control while leased = %q", m.Snapshot().Control)
	}
	// a second command within ReleaseAfter reuses the lease
	if _, err := m.Command(context.Background(), "set_head_angle", Args{"angle": []byte("0")}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "release", func() (ok bool) {
		robot.get(func() { ok = robot.releases == 1 })
		return ok
	})
	robot.get(func() {
		if robot.controls != 1 {
			t.Fatalf("lease not reused: %d control requests", robot.controls)
		}
	})
	eventually(t, "control none", func() bool { return m.Snapshot().Control == "none" })
}

func TestStopCancelsActionAndWheels(t *testing.T) {
	robot, _, m := start(t)
	done := make(chan error, 1)
	go func() {
		_, err := m.Command(context.Background(), "drive_wheels", Args{"left": []byte("50"), "right": []byte("50"), "duration": []byte("10")})
		done <- err
	}()
	eventually(t, "driving", func() (ok bool) {
		robot.get(func() { ok = len(robot.wheels) == 1 })
		return ok
	})
	if _, err := m.Command(context.Background(), "stop", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || err.Error() != "stopped" {
			t.Fatalf("drive_wheels after stop: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("drive_wheels not cancelled")
	}
	robot.get(func() {
		if robot.stops != 1 || len(robot.wheels) != 2 || robot.wheels[1] != 0 {
			t.Fatalf("stops=%d wheels=%v (wheels must be zeroed)", robot.stops, robot.wheels)
		}
	})
	eventually(t, "release after stop", func() (ok bool) {
		robot.get(func() { ok = robot.releases == 1 })
		return ok
	})
}

func TestControlLost(t *testing.T) {
	robot, sink, m := start(t)
	if err := m.lease.Hold(context.Background(), time.Minute); err != nil {
		t.Fatal(err)
	}
	eventually(t, "held", func() bool { return m.Snapshot().Control == "held" })
	robot.loseCtl <- struct{}{}
	eventually(t, "control_lost event", func() bool { return sink.has("control_lost") })
	eventually(t, "control none", func() bool { return m.Snapshot().Control == "none" })
	// the next command asks again
	if _, err := m.Command(context.Background(), "set_head_angle", Args{}); err != nil {
		t.Fatal(err)
	}
	robot.get(func() {
		if robot.controls != 2 {
			t.Fatalf("controls=%d", robot.controls)
		}
	})
}

func TestReconnectAfterStreamEnds(t *testing.T) {
	robot, _, m := start(t)
	robot.endStream <- struct{}{}
	eventually(t, "second stream", func() (ok bool) {
		robot.get(func() { ok = robot.streams >= 2 })
		return ok
	})
	eventually(t, "connected again", func() bool { return m.Snapshot().Connected })
}

func TestCommandWhileDisconnected(t *testing.T) {
	m := NewManager(Config{IP: "x", GUID: "y"}, slog.New(slog.NewTextHandler(io.Discard, nil)), &fakeSink{}, nil)
	if _, err := m.Command(context.Background(), "say_text", Args{"text": []byte(`"hi"`)}); err == nil {
		t.Fatal("command without a robot must fail")
	}
}

package main

// Experiments that make the robot act: behavior control, audio, camera, photos, faces, display, cubes.

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

func since(t0 time.Time) string { return fmt.Sprintf("+%6.2fs", time.Since(t0).Seconds()) }

// acquireControl opens a BehaviorControl stream and waits for the grant. The returned release()
// sends ControlRelease and closes the stream. Lost-control events are logged.
func acquireControl(ctx context.Context, cli vectorpb.ExternalInterfaceClient, t0 time.Time) (func(), error) {
	st, err := cli.BehaviorControl(ctx)
	if err != nil {
		return nil, err
	}
	req := &vectorpb.BehaviorControlRequest{RequestType: &vectorpb.BehaviorControlRequest_ControlRequest{
		ControlRequest: &vectorpb.ControlRequest{Priority: vectorpb.ControlRequest_DEFAULT}}}
	if err := st.Send(req); err != nil {
		return nil, err
	}
	granted := make(chan struct{})
	go func() {
		once := false
		for {
			r, err := st.Recv()
			if err != nil {
				log.Printf("%s control stream end: %v", since(t0), err)
				return
			}
			switch r.GetResponseType().(type) {
			case *vectorpb.BehaviorControlResponse_ControlGrantedResponse:
				log.Printf("%s control GRANTED", since(t0))
				if !once {
					once = true
					close(granted)
				}
			case *vectorpb.BehaviorControlResponse_KeepAlive:
			default:
				b, _ := pj.Marshal(r)
				log.Printf("%s control: %s", since(t0), b)
			}
		}
	}()
	select {
	case <-granted:
	case <-time.After(10 * time.Second):
		return nil, fmt.Errorf("no control grant within 10s")
	}
	return func() {
		_ = st.Send(&vectorpb.BehaviorControlRequest{RequestType: &vectorpb.BehaviorControlRequest_ControlRelease{
			ControlRelease: &vectorpb.ControlRelease{}}})
		log.Printf("%s control released", since(t0))
		time.Sleep(500 * time.Millisecond)
		_ = st.CloseSend()
	}, nil
}

// probeControl: take control, nod the head, say a sentence, hold, release.
func probeControl(ctx context.Context, cli vectorpb.ExternalInterfaceClient, hold time.Duration) {
	t0 := time.Now()
	release, err := acquireControl(ctx, cli, t0)
	if err != nil {
		log.Fatalf("control: %v", err)
	}
	for i, a := range []float32{0.7, -0.3, 0.2} {
		r, err := cli.SetHeadAngle(ctx, &vectorpb.SetHeadAngleRequest{AngleRad: a, MaxSpeedRadPerSec: 3, AccelRadPerSec2: 10, IdTag: int32(2000001 + i)})
		log.Printf("%s head %.1f → %v %v", since(t0), a, r.GetResult().GetCode(), err)
	}
	r, err := cli.SayText(ctx, &vectorpb.SayTextRequest{Text: "Hello there. Home Assistant is here.", UseVectorVoice: true, DurationScalar: 1})
	log.Printf("%s say → %v %v", since(t0), r.GetState(), err)
	log.Printf("%s holding %s (robot should stay still)", since(t0), hold)
	time.Sleep(hold)
	release()
	log.Printf("%s done — robot should resume its own behavior", since(t0))
}

// probeAudio: play raw PCM s16le 16 kHz mono from file at real-time pace (≤1 s ahead).
func probeAudio(ctx context.Context, cli vectorpb.ExternalInterfaceClient, file string, rate, volume uint32, withControl bool) {
	pcm, err := os.ReadFile(file)
	if err != nil {
		log.Fatal(err)
	}
	t0 := time.Now()
	if withControl {
		release, err := acquireControl(ctx, cli, t0)
		if err != nil {
			log.Fatalf("control: %v", err)
		}
		defer release()
	}
	st, err := cli.ExternalAudioStreamPlayback(ctx)
	if err != nil {
		log.Fatalf("audio stream: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			r, err := st.Recv()
			if err != nil {
				log.Printf("%s audio stream end: %v", since(t0), err)
				return
			}
			b, _ := pj.Marshal(r)
			log.Printf("%s audio response: %s", since(t0), b)
			if r.GetAudioStreamPlaybackComplete() != nil || r.GetAudioStreamPlaybackFailyer() != nil {
				return
			}
		}
	}()
	must := func(err error) {
		if err != nil {
			log.Fatalf("%s send: %v", since(t0), err)
		}
	}
	must(st.Send(&vectorpb.ExternalAudioStreamRequest{AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamPrepare{
		AudioStreamPrepare: &vectorpb.ExternalAudioStreamPrepare{AudioFrameRate: rate, AudioVolume: volume}}}))
	const chunk = 1024
	bytesPerSec := float64(rate * 2)
	start := time.Now()
	for off := 0; off < len(pcm); off += chunk {
		end := min(off+chunk, len(pcm))
		// pace: never more than 1 s of audio ahead of the wall clock
		ahead := time.Duration(float64(off)/bytesPerSec*float64(time.Second)) - time.Since(start)
		if ahead > time.Second {
			time.Sleep(ahead - time.Second)
		}
		must(st.Send(&vectorpb.ExternalAudioStreamRequest{AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamChunk{
			AudioStreamChunk: &vectorpb.ExternalAudioStreamChunk{AudioChunkSizeBytes: uint32(end - off), AudioChunkSamples: pcm[off:end]}}}))
	}
	log.Printf("%s sent %d bytes (%.1fs of audio)", since(t0), len(pcm), float64(len(pcm))/bytesPerSec)
	must(st.Send(&vectorpb.ExternalAudioStreamRequest{AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamComplete{
		AudioStreamComplete: &vectorpb.ExternalAudioStreamComplete{}}}))
	select {
	case <-done:
	case <-time.After(time.Duration(float64(len(pcm))/bytesPerSec*float64(time.Second)) + 10*time.Second):
		log.Printf("%s no completion response", since(t0))
	}
	_ = st.CloseSend()
}

// probeCamera: stream the camera for d, report fps, sizes, encodings; save the first frame.
func probeCamera(ctx context.Context, cli vectorpb.ExternalInterfaceClient, d time.Duration, out string, withControl bool) {
	t0 := time.Now()
	if withControl {
		release, err := acquireControl(ctx, cli, t0)
		if err != nil {
			log.Fatalf("control: %v", err)
		}
		defer release()
	}
	if os.Getenv("ENABLE_STREAMING") != "" {
		r, err := cli.EnableImageStreaming(ctx, &vectorpb.EnableImageStreamingRequest{Enable: true})
		log.Printf("%s EnableImageStreaming: %v %v", since(t0), r.GetStatus().GetCode(), err)
	}
	cctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	st, err := cli.CameraFeed(cctx, &vectorpb.CameraFeedRequest{})
	if err != nil {
		log.Fatalf("camera: %v", err)
	}
	n, total := 0, 0
	var first time.Time
	encs := map[string]int{}
	for {
		r, err := st.Recv()
		if err != nil {
			log.Printf("%s camera stream end: %v", since(t0), err)
			break
		}
		if n == 0 {
			first = time.Now()
			log.Printf("%s first frame: %d bytes, %v", since(t0), len(r.GetData()), r.GetImageEncoding())
			if out != "" {
				_ = os.WriteFile(out, r.GetData(), 0o644)
			}
		}
		n++
		total += len(r.GetData())
		encs[r.GetImageEncoding().String()]++
	}
	if n > 1 {
		secs := time.Since(first).Seconds()
		log.Printf("frames=%d fps=%.1f avg=%d B (%.0f kB/s) encodings=%v", n, float64(n-1)/secs, total/n, float64(total)/secs/1024, encs)
	}
}

// probeCapture: one frame with CaptureSingleImage (optionally high resolution).
func probeCapture(ctx context.Context, cli vectorpb.ExternalInterfaceClient, hires, withControl bool, out string) {
	t0 := time.Now()
	if withControl {
		release, err := acquireControl(ctx, cli, t0)
		if err != nil {
			log.Fatalf("control: %v", err)
		}
		defer release()
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	r, err := cli.CaptureSingleImage(cctx, &vectorpb.CaptureSingleImageRequest{EnableHighResolution: hires})
	if err != nil {
		log.Printf("%s capture: %v", since(t0), err)
		return
	}
	log.Printf("%s capture: %d bytes %v status=%v", since(t0), len(r.GetData()), r.GetImageEncoding(), r.GetStatus().GetCode())
	if out != "" && len(r.GetData()) > 0 {
		_ = os.WriteFile(out, r.GetData(), 0o644)
	}
}

// probePhotos: list photos stored on the robot and fetch the newest one + its thumbnail.
func probePhotos(ctx context.Context, cli vectorpb.ExternalInterfaceClient, out string) {
	r, err := cli.PhotosInfo(ctx, &vectorpb.PhotosInfoRequest{})
	if err != nil {
		log.Fatalf("photos: %v", err)
	}
	var newest *vectorpb.PhotoInfo
	for _, p := range r.GetPhotoInfos() {
		log.Printf("photo id=%d ts=%s", p.GetPhotoId(), time.Unix(int64(p.GetTimestampUtc()), 0).Format(time.RFC3339))
		if newest == nil || p.GetTimestampUtc() > newest.GetTimestampUtc() {
			newest = p
		}
	}
	if newest == nil {
		return
	}
	t0 := time.Now()
	ph, err := cli.Photo(ctx, &vectorpb.PhotoRequest{PhotoId: newest.GetPhotoId()})
	log.Printf("%s photo %d: %d bytes success=%v err=%v", since(t0), newest.GetPhotoId(), len(ph.GetImage()), ph.GetSuccess(), err)
	th, err := cli.Thumbnail(ctx, &vectorpb.ThumbnailRequest{PhotoId: newest.GetPhotoId()})
	log.Printf("%s thumb: %d bytes err=%v", since(t0), len(th.GetImage()), err)
	if out != "" {
		_ = os.WriteFile(out, ph.GetImage(), 0o644)
	}
}

// probeFaces: RequestEnrolledNames only (it was suspected of crashing the robot's SDK server).
func probeFaces(ctx context.Context, cli vectorpb.ExternalInterfaceClient) {
	t0 := time.Now()
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	r, err := cli.RequestEnrolledNames(cctx, &vectorpb.RequestEnrolledNamesRequest{})
	if err != nil {
		log.Printf("%s faces: %v", since(t0), err)
		return
	}
	for _, f := range r.GetFaces() {
		log.Printf("%s face id=%d name=%q last_seen=%d", since(t0), f.GetFaceId(), f.GetName(), f.GetLastSeenSecondsSinceEpoch())
	}
	log.Printf("%s %d faces", since(t0), len(r.GetFaces()))
}

// probeDisplay: show a raw RGB565 file for d, under behavior control.
// WARNING: crashes a Vector 2.0 (vic-anim error 914, see FINDINGS.md §9).
func probeDisplay(ctx context.Context, cli vectorpb.ExternalInterfaceClient, file string, d time.Duration, withControl bool) {
	data, err := os.ReadFile(file)
	if err != nil {
		log.Fatal(err)
	}
	t0 := time.Now()
	if withControl {
		release, err := acquireControl(ctx, cli, t0)
		if err != nil {
			log.Fatalf("control: %v", err)
		}
		defer release()
	}
	_, err = cli.DisplayFaceImageRGB(ctx, &vectorpb.DisplayFaceImageRGBRequest{FaceData: data, DurationMs: uint32(d.Milliseconds()), InterruptRunning: true})
	log.Printf("%s display (%d bytes): err=%v", since(t0), len(data), err)
	time.Sleep(d)
	log.Printf("%s display done", since(t0))
}

// probeCubes: BLE scan for cubes, then a connection attempt.
func probeCubes(ctx context.Context, cli vectorpb.ExternalInterfaceClient) {
	t0 := time.Now()
	if os.Getenv("CONTROL") != "" {
		release, err := acquireControl(ctx, cli, t0)
		if err != nil {
			log.Fatalf("control: %v", err)
		}
		defer release()
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, err := cli.CubesAvailable(cctx, &vectorpb.CubesAvailableRequest{})
	log.Printf("%s cubes available: %v err=%v", since(t0), r.GetFactoryIds(), err)
	c, err := cli.ConnectCube(cctx, &vectorpb.ConnectCubeRequest{})
	log.Printf("%s connect: success=%v id=%d factory=%q err=%v", since(t0), c.GetSuccess(), c.GetObjectId(), c.GetFactoryId(), err)
}

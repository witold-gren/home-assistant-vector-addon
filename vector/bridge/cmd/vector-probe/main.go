// vector-probe: a research tool that checks on a real robot what Vector's gRPC API gives (and what
// breaks it) — the findings are in FINDINGS.md. Runs on any computer in the robot's network.
//
//	go run ./cmd/vector-probe -wirepod http://escapepod.local:8080 creds
//	go run ./cmd/vector-probe -wirepod ... status
//	go run ./cmd/vector-probe -wirepod ... events -d 10m > events.jsonl
//
// The GUID is never printed.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type sdkInfo struct {
	GlobalGUID string `json:"global_guid"`
	Robots     []struct {
		ESN       string `json:"esn"`
		IPAddress string `json:"ip_address"`
		GUID      string `json:"guid"`
		Activated bool   `json:"activated"`
	} `json:"robots"`
}

type robotCreds struct{ esn, ip, guid, guidSource string }

func fetchCreds(wirepod, esn string) (robotCreds, error) {
	resp, err := http.Get(strings.TrimRight(wirepod, "/") + "/api-sdk/get_sdk_info")
	if err != nil {
		return robotCreds{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var info sdkInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return robotCreds{}, fmt.Errorf("get_sdk_info: %w (%.80s)", err, body)
	}
	for _, r := range info.Robots {
		if esn != "" && !strings.EqualFold(r.ESN, esn) {
			continue
		}
		c := robotCreds{esn: r.ESN, ip: r.IPAddress, guid: r.GUID, guidSource: "robot"}
		if c.guid == "" {
			c.guid, c.guidSource = info.GlobalGUID, "global"
		}
		return c, nil
	}
	return robotCreds{}, fmt.Errorf("no robot %q in wire-pod", esn)
}

type tokenAuth string

func (t tokenAuth) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(t)}, nil
}
func (tokenAuth) RequireTransportSecurity() bool { return true }

func dial(c robotCreds) (vectorpb.ExternalInterfaceClient, *grpc.ClientConn, error) {
	conn, err := grpc.NewClient(c.ip+":443",
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})),
		grpc.WithPerRPCCredentials(tokenAuth(c.guid)),
	)
	if err != nil {
		return nil, nil, err
	}
	return vectorpb.NewExternalInterfaceClient(conn), conn, nil
}

var pj = protojson.MarshalOptions{EmitUnpopulated: false, UseProtoNames: true}

func dump(kind string, m proto.Message) {
	b, _ := pj.Marshal(m)
	fmt.Printf("{\"ts\":%q,\"kind\":%q,\"msg\":%s}\n", time.Now().Format(time.RFC3339Nano), kind, b)
}

func main() {
	wirepod := flag.String("wirepod", "http://escapepod.local:8080", "wire-pod base URL")
	esn := flag.String("esn", "", "robot serial (default: first)")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		log.Fatal("subcommand: creds | status | events | control | audio | camera")
	}
	c, err := fetchCreds(*wirepod, *esn)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("robot esn=%s ip=%s guid=%s (len %d)", c.esn, c.ip, c.guidSource, len(c.guid))
	cli, conn, err := dial(c)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	ctx := context.Background()

	switch args[0] {
	case "creds", "status":
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		b, err := cli.BatteryState(cctx, &vectorpb.BatteryStateRequest{})
		if err != nil {
			log.Fatalf("BatteryState: %v", err)
		}
		dump("battery", b)
		if v, err := cli.VersionState(cctx, &vectorpb.VersionStateRequest{}); err == nil {
			dump("version", v)
		} else {
			log.Printf("VersionState: %v", err)
		}
		if args[0] == "status" {
			if j, err := cli.PullJdocs(cctx, &vectorpb.PullJdocsRequest{JdocTypes: []vectorpb.JdocType{
				vectorpb.JdocType_ROBOT_SETTINGS, vectorpb.JdocType_ROBOT_LIFETIME_STATS}}); err == nil {
				dump("jdocs", j)
			} else {
				log.Printf("PullJdocs: %v", err)
			}
		}
	case "events":
		fs := flag.NewFlagSet("events", flag.ExitOnError)
		d := fs.Duration("d", 2*time.Minute, "duration")
		_ = fs.Parse(args[1:])
		ectx, cancel := context.WithTimeout(ctx, *d)
		defer cancel()
		st, err := cli.EventStream(ectx, &vectorpb.EventRequest{})
		if err != nil {
			log.Fatalf("EventStream: %v", err)
		}
		// robot_state arrives many times a second: print it only when status / touch / carrying
		// changes, plus every 30 s as a sample.
		var lastKey string
		var lastState time.Time
		counts := map[string]int{}
		for {
			r, err := st.Recv()
			if err != nil {
				log.Printf("stream end: %v", err)
				break
			}
			ev := r.GetEvent()
			if ev == nil {
				dump("other", r)
				continue
			}
			kind := fmt.Sprintf("%T", ev.GetEventType())
			kind = strings.TrimPrefix(kind, "*vectorpb.Event_")
			counts[kind]++
			if rs := ev.GetRobotState(); rs != nil {
				key := fmt.Sprintf("%d|%v|%d", rs.GetStatus(), rs.GetTouchData().GetIsBeingTouched(), rs.GetCarryingObjectId())
				if key == lastKey && time.Since(lastState) < 30*time.Second {
					continue
				}
				lastKey, lastState = key, time.Now()
			}
			dump(kind, ev)
		}
		cb, _ := json.Marshal(counts)
		fmt.Fprintf(os.Stderr, "event counts: %s\n", cb)
	case "control":
		fs := flag.NewFlagSet("control", flag.ExitOnError)
		hold := fs.Duration("hold", 5*time.Second, "how long to hold control after the actions")
		_ = fs.Parse(args[1:])
		probeControl(ctx, cli, *hold)
	case "audio":
		fs := flag.NewFlagSet("audio", flag.ExitOnError)
		f := fs.String("f", "", "raw PCM s16le mono file")
		rate := fs.Uint("rate", 16000, "sample rate")
		vol := fs.Uint("vol", 60, "volume 0-100")
		ctl := fs.Bool("control", false, "take behavior control first")
		_ = fs.Parse(args[1:])
		probeAudio(ctx, cli, *f, uint32(*rate), uint32(*vol), *ctl)
	case "camera":
		fs := flag.NewFlagSet("camera", flag.ExitOnError)
		d := fs.Duration("d", 10*time.Second, "duration")
		out := fs.String("o", "", "save first frame here")
		ctl := fs.Bool("control", false, "take behavior control first")
		_ = fs.Parse(args[1:])
		probeCamera(ctx, cli, *d, *out, *ctl)
	case "capture":
		fs := flag.NewFlagSet("capture", flag.ExitOnError)
		hires := fs.Bool("hires", false, "high resolution")
		ctl := fs.Bool("control", false, "take behavior control first")
		out := fs.String("o", "", "save image here")
		_ = fs.Parse(args[1:])
		probeCapture(ctx, cli, *hires, *ctl, *out)
	case "photos":
		fs := flag.NewFlagSet("photos", flag.ExitOnError)
		out := fs.String("o", "", "save the newest photo here")
		_ = fs.Parse(args[1:])
		probePhotos(ctx, cli, *out)
	case "display":
		fs := flag.NewFlagSet("display", flag.ExitOnError)
		f := fs.String("f", "", "raw RGB565 file")
		d := fs.Duration("d", 5*time.Second, "duration")
		ctl := fs.Bool("control", true, "take behavior control first")
		_ = fs.Parse(args[1:])
		probeDisplay(ctx, cli, *f, *d, *ctl)
	case "cubes":
		probeCubes(ctx, cli)
	case "faces":
		probeFaces(ctx, cli)
	default:
		log.Fatalf("unknown subcommand %q", args[0])
	}
}

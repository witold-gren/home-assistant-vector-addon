package robot

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/witold-gren/home-assistant-vector-addon/bridge/internal/wirepod"
)

// Config of one robot. IP and GUID override what wire-pod reports.
type Config struct {
	ESN, Name, IP, GUID string
	Wirepod             *wirepod.Client
	ReleaseAfter        time.Duration // keep control this long after the last command
	BatteryEvery        time.Duration
	JdocsEvery          time.Duration
	Decode              Decoder // audio decoder (default ffmpeg)
}

// Sink receives state snapshots (coalesced, at most once a second) and events (immediately).
type Sink interface {
	State(State)
	Event(esn string, ev Event)
}

// Dialer opens the gRPC client; replaced in tests.
type Dialer func(ip, guid string) (vectorpb.ExternalInterfaceClient, func(), error)

var backoff = []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second, 60 * time.Second}

const streamSilence = 20 * time.Second // KeepAlive arrives every second; silence = dead link

type Manager struct {
	cfg  Config
	log  *slog.Logger
	sink Sink
	dial Dialer

	mu    sync.Mutex
	tr    *Tracker
	dirty bool
	cli   vectorpb.ExternalInterfaceClient

	lease  *Lease
	tags   atomic.Int32
	action sync.Mutex // movement/animation/speech commands run one at a time
	actMu  sync.Mutex
	actCtl context.CancelFunc

	animations []string // cached ListAnimations
	triggers   []string

	restIP, restGUID string // REST gateway of the connected robot

	Camera     *Camera
	Player     *Player
	mover      mover
	photosRead bool // the first photo read after start only sets the baseline
}

func NewManager(cfg Config, log *slog.Logger, sink Sink, dial Dialer) *Manager {
	if dial == nil {
		dial = DialGRPC
	}
	if cfg.ReleaseAfter == 0 {
		cfg.ReleaseAfter = 2 * time.Second
	}
	if cfg.BatteryEvery == 0 {
		cfg.BatteryEvery = time.Minute
	}
	if cfg.JdocsEvery == 0 {
		cfg.JdocsEvery = 10 * time.Minute
	}
	if cfg.Name == "" {
		cfg.Name = "Vector"
	}
	m := &Manager{cfg: cfg, log: log, sink: sink, dial: dial, tr: NewTracker(cfg.ESN, cfg.Name, cfg.IP)}
	m.tags.Store(sdkTagFirst - 1)
	m.Camera = newCamera(m)
	m.Player = newPlayer(m, cfg.Decode)
	m.lease = NewLease(log, cfg.ReleaseAfter, func(mode string) {
		m.update(func(t *Tracker) bool {
			if t.S.Control == mode {
				return false
			}
			t.S.Control = mode
			return true
		})
	}, func() { m.sink.Event(m.ESN(), Event{Name: "control_lost"}) })
	return m
}

// DialGRPC connects to the robot's SDK port (self-signed cert, GUID as bearer token).
func DialGRPC(ip, guid string) (vectorpb.ExternalInterfaceClient, func(), error) {
	conn, err := grpc.NewClient(ip+":443",
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})),
		grpc.WithPerRPCCredentials(bearer(guid)),
	)
	if err != nil {
		return nil, nil, err
	}
	return vectorpb.NewExternalInterfaceClient(conn), func() { _ = conn.Close() }, nil
}

type bearer string

func (b bearer) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(b)}, nil
}
func (bearer) RequireTransportSecurity() bool { return true }

// ESN of the robot (known after the first wire-pod lookup when not configured).
func (m *Manager) ESN() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tr.S.ESN
}

// Snapshot of the current state.
func (m *Manager) Snapshot() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

func (m *Manager) snapshotLocked() State {
	s := m.tr.S
	if s.Battery != nil {
		b := *s.Battery
		s.Battery = &b
	}
	return s
}

// update mutates the tracker; a change is pushed on the next flush.
func (m *Manager) update(f func(*Tracker) bool) {
	m.mu.Lock()
	if f(m.tr) {
		m.dirty = true
	}
	m.mu.Unlock()
}

func (m *Manager) client() (vectorpb.ExternalInterfaceClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cli == nil {
		return nil, errors.New("robot not connected")
	}
	return m.cli, nil
}

// Run keeps the robot connected until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	go m.flushLoop(ctx)
	attempt := 0
	for ctx.Err() == nil {
		start := time.Now()
		err := m.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			attempt = 0
		}
		delay := backoff[min(attempt, len(backoff)-1)]
		attempt++
		m.log.Warn("robot disconnected", "err", err, "retry_in", delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

func (m *Manager) resolve(ctx context.Context) (esn, ip, guid string, err error) {
	esn, ip, guid = m.cfg.ESN, m.cfg.IP, m.cfg.GUID
	if ip != "" && guid != "" || m.cfg.Wirepod == nil {
		if ip == "" || guid == "" {
			return "", "", "", errors.New("no wire-pod and no ip/guid configured")
		}
		return esn, ip, guid, nil
	}
	r, err := m.cfg.Wirepod.Robot(ctx, esn)
	if err != nil {
		return "", "", "", err
	}
	if ip == "" {
		ip = r.IP
	}
	if guid == "" {
		guid = r.GUID
	}
	return r.ESN, ip, guid, nil
}

// session: one connection, from credentials to the end of the event stream.
func (m *Manager) session(ctx context.Context) error {
	esn, ip, guid, err := m.resolve(ctx)
	if err != nil {
		return err
	}
	m.update(func(t *Tracker) bool {
		changed := t.S.ESN != esn || t.S.IP != ip
		t.S.ESN, t.S.IP = esn, ip
		return changed
	})
	cli, closeConn, err := m.dial(ip, guid)
	if err != nil {
		return err
	}
	defer closeConn()

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cctx, ccancel := context.WithTimeout(sctx, 10*time.Second)
	b, err := cli.BatteryState(cctx, &vectorpb.BatteryStateRequest{})
	ccancel()
	if err != nil {
		if status.Code(err) == codes.Unauthenticated {
			return errors.New("robot rejected the GUID (re-read from wire-pod on retry)")
		}
		return err
	}
	stream, err := cli.EventStream(sctx, &vectorpb.EventRequest{})
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.cli = cli
	m.restIP, m.restGUID = ip, guid
	m.tr.ApplyBattery(b)
	m.tr.S.Connected = true
	m.dirty = true
	m.mu.Unlock()
	m.lease.SetClient(cli)
	m.log.Info("robot connected", "esn", esn, "ip", ip)
	defer func() {
		m.lease.SetClient(nil)
		m.mu.Lock()
		m.cli = nil
		m.tr.S.Connected = false
		m.tr.S.Listening = false
		m.dirty = true
		m.mu.Unlock()
	}()

	go m.pollVersion(sctx, cli)
	go m.poll(sctx, cli)

	// The stream blocks in Recv; a watchdog cancels it when the robot goes silent.
	alive := make(chan struct{}, 1)
	go func() {
		t := time.NewTimer(streamSilence)
		defer t.Stop()
		for {
			select {
			case <-sctx.Done():
				return
			case <-alive:
				t.Reset(streamSilence)
			case <-t.C:
				m.log.Warn("event stream silent, reconnecting")
				cancel()
				return
			}
		}
	}()
	for {
		r, err := stream.Recv()
		if err != nil {
			return err
		}
		select {
		case alive <- struct{}{}:
		default:
		}
		ev := r.GetEvent()
		if ev == nil {
			continue
		}
		m.mu.Lock()
		changed, events := m.tr.ApplyEvent(ev, time.Now())
		if changed {
			m.dirty = true
		}
		esn := m.tr.S.ESN
		m.mu.Unlock()
		for _, e := range events {
			m.sink.Event(esn, e)
		}
	}
}

func (m *Manager) pollVersion(ctx context.Context, cli vectorpb.ExternalInterfaceClient) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if v, err := cli.VersionState(cctx, &vectorpb.VersionStateRequest{}); err == nil {
		m.update(func(t *Tracker) bool {
			changed := t.S.Firmware != v.GetOsVersion()
			t.S.Firmware = v.GetOsVersion()
			return changed
		})
	}
}

func (m *Manager) poll(ctx context.Context, cli vectorpb.ExternalInterfaceClient) {
	m.refreshJdocs(ctx, cli)
	_, _ = m.refreshFaces(ctx, cli)
	m.refreshPhotos(ctx, cli)
	battery := time.NewTicker(m.cfg.BatteryEvery)
	jdocs := time.NewTicker(m.cfg.JdocsEvery)
	defer battery.Stop()
	defer jdocs.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-battery.C:
			m.refreshBattery(ctx, cli)
			m.refreshPhotos(ctx, cli)
		case <-jdocs.C:
			m.refreshJdocs(ctx, cli)
			_, _ = m.refreshFaces(ctx, cli)
		}
	}
}

func (m *Manager) refreshBattery(ctx context.Context, cli vectorpb.ExternalInterfaceClient) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if b, err := cli.BatteryState(cctx, &vectorpb.BatteryStateRequest{}); err == nil {
		m.update(func(t *Tracker) bool { return t.ApplyBattery(b) })
	} else {
		m.log.Debug("battery poll", "err", err)
	}
}

func (m *Manager) refreshJdocs(ctx context.Context, cli vectorpb.ExternalInterfaceClient) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	j, err := cli.PullJdocs(cctx, &vectorpb.PullJdocsRequest{JdocTypes: []vectorpb.JdocType{
		vectorpb.JdocType_ROBOT_SETTINGS, vectorpb.JdocType_ROBOT_LIFETIME_STATS}})
	if err != nil {
		m.log.Debug("jdocs poll", "err", err)
		return
	}
	m.update(func(t *Tracker) bool { return t.ApplyJdocs(j) })
}

// flushLoop pushes the state at most once a second and ends touches that stopped.
func (m *Manager) flushLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			m.mu.Lock()
			if c, _ := m.tr.Tick(now); c {
				m.dirty = true
			}
			var s *State
			if m.dirty {
				m.dirty = false
				m.tr.S.Updated = now.Unix()
				snap := m.snapshotLocked()
				s = &snap
			}
			m.mu.Unlock()
			if s != nil {
				m.sink.State(*s)
			}
		}
	}
}

// rest calls the robot's REST gateway (same port, self-signed cert, GUID as bearer token).
func (m *Manager) rest(ctx context.Context, path string, body any) error {
	m.mu.Lock()
	ip, guid := m.restIP, m.restGUID
	m.mu.Unlock()
	if ip == "" {
		return errors.New("robot not connected")
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+ip+":443"+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+guid)
	req.Header.Set("Content-Type", "application/json")
	resp, err := restClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("robot %s: HTTP %d %s", path, resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return nil
}

var restClient = &http.Client{
	Timeout:   15 * time.Second,
	Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
}

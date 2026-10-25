package robot

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

// ErrControlLost: Vector (or another SDK client) took behavior control back mid-command.
var ErrControlLost = errors.New("behavior control lost")

// Lease holds BehaviorControl only while commands need it: taken on the first command, released
// `releaseAfter` after the last one finishes, or kept until `Hold` expires. Vector is never left
// frozen: a lost connection or ControlLost drops the lease, and Reset releases it.
type Lease struct {
	log          *slog.Logger
	releaseAfter time.Duration
	onMode       func(mode string) // none | auto | held
	onLost       func()

	mu        sync.Mutex
	cli       vectorpb.ExternalInterfaceClient
	stream    vectorpb.ExternalInterface_BehaviorControlClient
	cancel    context.CancelFunc
	granted   chan struct{} // closed on grant
	lost      chan struct{} // closed when this stream ends or control is lost
	inflight  int
	holdUntil time.Time
	timer     *time.Timer
}

func NewLease(log *slog.Logger, releaseAfter time.Duration, onMode func(string), onLost func()) *Lease {
	return &Lease{log: log, releaseAfter: releaseAfter, onMode: onMode, onLost: onLost}
}

// SetClient is called on every (re)connect; nil drops the lease without talking to the robot.
func (l *Lease) SetClient(cli vectorpb.ExternalInterfaceClient) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dropLocked()
	l.cli = cli
	l.holdUntil = time.Time{}
	l.inflight = 0
	l.modeLocked()
}

// Acquire waits until control is granted. `done` must be called when the command finishes; `lost`
// closes if control is taken away meanwhile.
func (l *Lease) Acquire(ctx context.Context) (done func(), lost <-chan struct{}, err error) {
	l.mu.Lock()
	if l.cli == nil {
		l.mu.Unlock()
		return nil, nil, errors.New("robot not connected")
	}
	l.inflight++
	l.stopTimerLocked()
	if l.stream == nil {
		if err := l.openLocked(); err != nil {
			l.inflight--
			l.scheduleLocked()
			l.mu.Unlock()
			return nil, nil, err
		}
	}
	granted, lostCh := l.granted, l.lost
	l.modeLocked()
	l.mu.Unlock()

	var once sync.Once
	done = func() {
		once.Do(func() {
			l.mu.Lock()
			l.inflight--
			l.scheduleLocked()
			l.mu.Unlock()
		})
	}
	// a robot dozing on the charger took ~6 s to grant (FINDINGS.md)
	wait, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	select {
	case <-granted:
		return done, lostCh, nil
	case <-lostCh:
		done()
		return nil, nil, ErrControlLost
	case <-wait.Done():
		done()
		return nil, nil, errors.New("behavior control not granted")
	}
}

// Hold keeps control for d (manual mode, e.g. driving from a dashboard).
func (l *Lease) Hold(ctx context.Context, d time.Duration) error {
	done, _, err := l.Acquire(ctx)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.holdUntil = time.Now().Add(d)
	l.mu.Unlock()
	done()
	return nil
}

// Release gives control back now (unless commands are still running: then right after them).
func (l *Lease) Release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.holdUntil = time.Time{}
	if l.inflight == 0 {
		l.releaseLocked()
	}
}

func (l *Lease) openLocked() error {
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := l.cli.BehaviorControl(ctx)
	if err != nil {
		cancel()
		return err
	}
	err = stream.Send(&vectorpb.BehaviorControlRequest{RequestType: &vectorpb.BehaviorControlRequest_ControlRequest{
		ControlRequest: &vectorpb.ControlRequest{Priority: vectorpb.ControlRequest_DEFAULT}}})
	if err != nil {
		cancel()
		return err
	}
	l.stream, l.cancel = stream, cancel
	l.granted, l.lost = make(chan struct{}), make(chan struct{})
	go l.read(stream, l.granted, l.lost)
	return nil
}

func (l *Lease) read(stream vectorpb.ExternalInterface_BehaviorControlClient, granted, lost chan struct{}) {
	grantedOnce := false
	for {
		r, err := stream.Recv()
		if err != nil {
			l.ended(stream, lost, false)
			return
		}
		switch r.GetResponseType().(type) {
		case *vectorpb.BehaviorControlResponse_ControlGrantedResponse:
			if !grantedOnce {
				grantedOnce = true
				close(granted)
				l.log.Debug("behavior control granted")
			}
		case *vectorpb.BehaviorControlResponse_ControlLostEvent, *vectorpb.BehaviorControlResponse_ReservedControlLostEvent:
			l.log.Info("behavior control lost to the robot")
			l.ended(stream, lost, true)
			return
		}
	}
}

// ended forgets `stream` if it is still the current one.
func (l *Lease) ended(stream vectorpb.ExternalInterface_BehaviorControlClient, lost chan struct{}, wasLost bool) {
	l.mu.Lock()
	current := l.stream == stream
	if current {
		l.dropLocked()
		l.holdUntil = time.Time{}
		l.modeLocked()
	}
	l.mu.Unlock()
	select {
	case <-lost:
	default:
		close(lost)
	}
	if current && wasLost && l.onLost != nil {
		l.onLost()
	}
}

func (l *Lease) releaseLocked() {
	if l.stream == nil {
		return
	}
	_ = l.stream.Send(&vectorpb.BehaviorControlRequest{RequestType: &vectorpb.BehaviorControlRequest_ControlRelease{
		ControlRelease: &vectorpb.ControlRelease{}}})
	_ = l.stream.CloseSend()
	l.log.Debug("behavior control released")
	// give the release a moment to reach the robot before cancelling the stream
	stream, cancel := l.stream, l.cancel
	l.stream, l.cancel = nil, nil
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
		_ = stream
	}()
	l.modeLocked()
}

func (l *Lease) dropLocked() {
	l.stopTimerLocked()
	if l.cancel != nil {
		l.cancel()
	}
	l.stream, l.cancel = nil, nil
}

func (l *Lease) stopTimerLocked() {
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
}

// scheduleLocked arms the release timer once nothing runs.
func (l *Lease) scheduleLocked() {
	l.modeLocked()
	if l.inflight > 0 || l.stream == nil {
		return
	}
	l.stopTimerLocked()
	wait := l.releaseAfter
	if until := time.Until(l.holdUntil); until > wait {
		wait = until
	}
	stream := l.stream
	l.timer = time.AfterFunc(wait, func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.stream == stream && l.inflight == 0 && !time.Now().Before(l.holdUntil) {
			l.releaseLocked()
		}
	})
}

func (l *Lease) modeLocked() {
	mode := "none"
	switch {
	case l.stream != nil && time.Now().Before(l.holdUntil):
		mode = "held"
	case l.stream != nil:
		mode = "auto"
	}
	if l.onMode != nil {
		l.onMode(mode)
	}
}

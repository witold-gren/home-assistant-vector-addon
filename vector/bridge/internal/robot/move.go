package robot

import (
	"context"
	"sync"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

// Driving from a dashboard: while a button is held the card repeats `move` every ~300 ms; each call
// sets wheel / head / lift speeds and re-arms a dead man's switch. When the calls stop (button
// released, page closed, network gone) everything stops after moveDeadman. Behavior control is
// taken on the first move and given back by the lease after the motors stop.
var moveDeadman = 700 * time.Millisecond

type mover struct {
	mu      sync.Mutex
	done    func() // lease held while moving
	timer   *time.Timer
	session int // a lost lease of an older session must not stop a newer one
}

// move: args left/right (mm/s, −220…220), head/lift (rad/s, −2…2); missing = 0.
func (m *Manager) move(ctx context.Context, cli vectorpb.ExternalInterfaceClient, a Args) error {
	left := float32(clamp(a.num("left", 0), -220, 220))
	right := float32(clamp(a.num("right", 0), -220, 220))
	head := float32(clamp(a.num("head", 0), -2, 2))
	lift := float32(clamp(a.num("lift", 0), -2, 2))
	still := left == 0 && right == 0 && head == 0 && lift == 0

	mv := &m.mover
	mv.mu.Lock()
	defer mv.mu.Unlock()
	if still {
		m.stopMotorsLocked(cli)
		return nil
	}
	if mv.done == nil {
		done, lost, err := m.lease.Acquire(ctx)
		if err != nil {
			return err
		}
		mv.done = done
		mv.session++
		session := mv.session
		go func() {
			<-lost
			mv.mu.Lock()
			defer mv.mu.Unlock()
			if mv.done != nil && mv.session == session {
				m.stopMotorsLocked(cli)
			}
		}()
	}
	if _, err := cli.DriveWheels(ctx, &vectorpb.DriveWheelsRequest{LeftWheelMmps: left, RightWheelMmps: right, LeftWheelMmps2: 400, RightWheelMmps2: 400}); err != nil {
		return err
	}
	if _, err := cli.MoveHead(ctx, &vectorpb.MoveHeadRequest{SpeedRadPerSec: head}); err != nil {
		return err
	}
	if _, err := cli.MoveLift(ctx, &vectorpb.MoveLiftRequest{SpeedRadPerSec: lift}); err != nil {
		return err
	}
	if mv.timer != nil {
		mv.timer.Stop()
	}
	mv.timer = time.AfterFunc(moveDeadman, func() {
		mv.mu.Lock()
		defer mv.mu.Unlock()
		m.stopMotorsLocked(cli)
	})
	return nil
}

// stopMotorsLocked zeroes all motors and gives the lease back (mover.mu held).
func (m *Manager) stopMotorsLocked(cli vectorpb.ExternalInterfaceClient) {
	mv := &m.mover
	if mv.timer != nil {
		mv.timer.Stop()
		mv.timer = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = cli.DriveWheels(ctx, &vectorpb.DriveWheelsRequest{})
	_, _ = cli.MoveHead(ctx, &vectorpb.MoveHeadRequest{})
	_, _ = cli.MoveLift(ctx, &vectorpb.MoveLiftRequest{})
	if mv.done != nil {
		mv.done()
		mv.done = nil
	}
}

package robot

import (
	"context"
	"testing"
	"time"
)

func TestMoveDeadman(t *testing.T) {
	moveDeadman = 150 * time.Millisecond
	robot, _, m := start(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := m.Command(ctx, "move", Args{"left": []byte("100"), "right": []byte("100")}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	robot.get(func() {
		if robot.controls != 1 || len(robot.wheels) != 3 || robot.wheels[2] != 100 {
			t.Fatalf("controls=%d wheels=%v", robot.controls, robot.wheels)
		}
	})
	// no more calls: the dead man's switch stops the wheels and the lease is given back
	eventually(t, "stop", func() (ok bool) {
		robot.get(func() { ok = len(robot.wheels) == 4 && robot.wheels[3] == 0 })
		return ok
	})
	eventually(t, "release", func() (ok bool) {
		robot.get(func() { ok = robot.releases == 1 })
		return ok
	})
}

func TestMoveZeroStopsAtOnce(t *testing.T) {
	moveDeadman = 5 * time.Second
	robot, _, m := start(t)
	ctx := context.Background()
	_, _ = m.Command(ctx, "move", Args{"head": []byte("1")})
	_, _ = m.Command(ctx, "move", Args{})
	robot.get(func() {
		if n := len(robot.heads); n != 2 || robot.heads[0] != 1 || robot.heads[1] != 0 {
			t.Fatalf("heads=%v", robot.heads)
		}
	})
	eventually(t, "release", func() (ok bool) {
		robot.get(func() { ok = robot.releases == 1 })
		return ok
	})
}

package robot

import (
	"context"
	"math"
	"testing"
)

func TestFaces(t *testing.T) {
	robot, _, m := start(t)
	robot.get(func() { robot.faces = map[int32]string{1: "Alice", 2: "Bob"} })
	ctx := context.Background()
	if _, err := m.Command(ctx, "list_faces", nil); err != nil {
		t.Fatal(err)
	}
	// sorted by name
	if f := m.Snapshot().Faces; len(f) != 2 || f[0].Name != "Alice" || f[1].Name != "Bob" {
		t.Fatalf("faces %+v", f)
	}
	if _, err := m.Command(ctx, "rename_face", Args{"face_id": []byte("2"), "name": []byte(`"Aaron"`)}); err != nil {
		t.Fatal(err)
	}
	if f := m.Snapshot().Faces; f[0].Name != "Aaron" || f[0].ID != 2 {
		t.Fatalf("after rename %+v", f)
	}
	if _, err := m.Command(ctx, "rename_face", Args{"face_id": []byte("9"), "name": []byte(`"x"`)}); err == nil {
		t.Fatal("unknown face must fail")
	}
}

func TestGoToRelative(t *testing.T) {
	robot, _, m := start(t)
	// Vector at (100, 0) facing +90° (along +y)
	m.update(func(tr *Tracker) bool { tr.S.Pose = &Pose{X: 100, Y: 0, Angle: 90}; return true })
	if _, err := m.Command(context.Background(), "go_to", Args{"x": []byte("200"), "angle": []byte("90")}); err != nil {
		t.Fatal(err)
	}
	robot.get(func() {
		g := robot.goTo[0]
		if math.Abs(float64(g[0])-100) > 0.01 || math.Abs(float64(g[1])-200) > 0.01 || math.Abs(float64(g[2])-math.Pi) > 0.001 {
			t.Fatalf("go_to %v (forward 200 mm when facing +y → (100, 200), angle 180°)", g)
		}
	})
}

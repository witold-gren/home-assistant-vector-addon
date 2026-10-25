package robot

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

// extraCommand handles faces, cube, navigation and the extra settings.
//
// No face-screen images: DisplayFaceImageRGB crashes this robot (Vector 2.0, firmware 2.0.1.6076ep):
// the SDK server still slices 184×96 images (Vector 1.0) and a 184×96 image kills vic-anim
// (error 914), a 160×80 one is rejected — tested 2026-10-23.
// ok=false: not one of these commands.
func (m *Manager) extraCommand(ctx context.Context, cli vectorpb.ExternalInterfaceClient, name string, a Args) (res any, err error, ok bool) {
	ok = true
	switch name {
	// ---- faces ----
	case "list_faces":
		res, err = m.refreshFaces(ctx, cli)
	case "rename_face":
		id := int32(a.num("face_id", -1))
		face, found := m.knownFace(id)
		if !found {
			return nil, fmt.Errorf("no face %d", id), true
		}
		newName := strings.TrimSpace(a.str("name", ""))
		if newName == "" {
			return nil, errors.New("name required"), true
		}
		if _, err = cli.UpdateEnrolledFaceByID(ctx, &vectorpb.UpdateEnrolledFaceByIDRequest{FaceId: id, OldName: face.Name, NewName: newName}); err == nil {
			_, err = m.refreshFaces(ctx, cli)
		}
	case "delete_face":
		id := int32(a.num("face_id", -1))
		if _, found := m.knownFace(id); !found {
			return nil, fmt.Errorf("no face %d", id), true
		}
		if _, err = cli.EraseEnrolledFaceByID(ctx, &vectorpb.EraseEnrolledFaceByIDRequest{FaceId: id}); err == nil {
			_, err = m.refreshFaces(ctx, cli)
		}
	case "enroll_face":
		// like the app's "meet": Vector looks for a face, asks to hold still and says the name
		faceName := strings.TrimSpace(a.str("name", ""))
		if faceName == "" {
			return nil, errors.New("name required"), true
		}
		if _, err = cli.SetFaceToEnroll(ctx, &vectorpb.SetFaceToEnrollRequest{Name: faceName, ObservedId: -1, SaveId: 0, SaveToRobot: true, SayName: true}); err == nil {
			_, err = cli.AppIntent(ctx, &vectorpb.AppIntentRequest{Intent: "intent_meet_victor", Param: faceName})
			go func() {
				time.Sleep(30 * time.Second)
				_, _ = m.refreshFaces(context.Background(), cli)
			}()
		}

	// ---- cube ----
	case "connect_cube":
		var r *vectorpb.ConnectCubeResponse
		if r, err = cli.ConnectCube(ctx, &vectorpb.ConnectCubeRequest{}); err == nil {
			if !r.GetSuccess() {
				return nil, errors.New("no cube found nearby"), true
			}
			m.update(func(t *Tracker) bool {
				c := t.cube()
				c.Connected, c.ObjectID, c.FactoryID = true, r.GetObjectId(), r.GetFactoryId()
				return true
			})
			res = map[string]any{"object_id": r.GetObjectId(), "factory_id": r.GetFactoryId()}
		}
	case "disconnect_cube":
		_, err = cli.DisconnectCube(ctx, &vectorpb.DisconnectCubeRequest{})
	case "flash_cube":
		_, err = cli.FlashCubeLights(ctx, &vectorpb.FlashCubeLightsRequest{})
	case "forget_cube":
		_, err = cli.ForgetPreferredCube(ctx, &vectorpb.ForgetPreferredCubeRequest{})
		m.update(func(t *Tracker) bool { t.S.Cube = nil; return true })

	case "set_clock_24h":
		err = m.updateSettings(ctx, cli, Args{"clock_24_hour": []byte(fmt.Sprint(a.boolean("on", true)))})
	default:
		ok = false
	}
	return res, err, ok
}

func (m *Manager) knownFace(id int32) (KnownFace, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.tr.S.Faces {
		if f.ID == id {
			return f, true
		}
	}
	return KnownFace{}, false
}

// refreshFaces reads the enrolled faces into the state.
func (m *Manager) refreshFaces(ctx context.Context, cli vectorpb.ExternalInterfaceClient) ([]KnownFace, error) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r, err := cli.RequestEnrolledNames(cctx, &vectorpb.RequestEnrolledNamesRequest{})
	if err != nil {
		return nil, err
	}
	faces := make([]KnownFace, 0, len(r.GetFaces()))
	for _, f := range r.GetFaces() {
		faces = append(faces, KnownFace{ID: f.GetFaceId(), Name: f.GetName(), LastSeen: f.GetLastSeenSecondsSinceEpoch()})
	}
	sort.Slice(faces, func(i, j int) bool { return faces[i].Name < faces[j].Name })
	m.update(func(t *Tracker) bool {
		if fmt.Sprint(t.S.Faces) == fmt.Sprint(faces) {
			return false
		}
		t.S.Faces = faces
		return true
	})
	return faces, nil
}

func (m *Manager) cubeID() (int32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.tr.S.Cube; c != nil && c.Connected {
		return int32(c.ObjectID), nil
	}
	return 0, errors.New("cube not connected (connect it first)")
}

func init() {
	// ---- navigation ----
	actions["go_to"] = func(ctx context.Context, m *Manager, cli vectorpb.ExternalInterfaceClient, a Args) (any, error) {
		x, y, ang := a.num("x", 0), a.num("y", 0), a.num("angle", 0)
		if a.boolean("relative", true) {
			// relative to where Vector stands now: x forward, y left (mm), angle left (°)
			m.mu.Lock()
			p := m.tr.S.Pose
			m.mu.Unlock()
			if p == nil {
				return nil, errors.New("robot position not known yet")
			}
			rad := p.Angle * math.Pi / 180
			x, y = p.X+x*math.Cos(rad)-y*math.Sin(rad), p.Y+x*math.Sin(rad)+y*math.Cos(rad)
			ang = p.Angle + ang
		}
		r, err := cli.GoToPose(ctx, &vectorpb.GoToPoseRequest{XMm: float32(x), YMm: float32(y), Rad: deg2rad(ang), IdTag: m.nextTag()})
		return actionResult(r.GetResult(), err)
	}
	actions["turn_towards_face"] = func(ctx context.Context, m *Manager, cli vectorpb.ExternalInterfaceClient, a Args) (any, error) {
		id := int32(a.num("face_id", -1))
		if id < 0 {
			m.mu.Lock()
			if f := m.tr.S.LastFace; f != nil {
				id = f.ID
			}
			m.mu.Unlock()
		}
		if id < 0 {
			return nil, errors.New("no face seen yet")
		}
		r, err := cli.TurnTowardsFace(ctx, &vectorpb.TurnTowardsFaceRequest{FaceId: id, MaxTurnAngleRad: deg2rad(180), IdTag: m.nextTag()})
		return actionResult(r.GetResult(), err)
	}
	cubeAction := func(do func(ctx context.Context, cli vectorpb.ExternalInterfaceClient, id int32, tag int32, a Args) (*vectorpb.ActionResult, error)) action {
		return func(ctx context.Context, m *Manager, cli vectorpb.ExternalInterfaceClient, a Args) (any, error) {
			id, err := m.cubeID()
			if err != nil {
				return nil, err
			}
			r, err := do(ctx, cli, id, m.nextTag(), a)
			return actionResult(r, err)
		}
	}
	actions["go_to_cube"] = cubeAction(func(ctx context.Context, cli vectorpb.ExternalInterfaceClient, id, tag int32, a Args) (*vectorpb.ActionResult, error) {
		r, err := cli.GoToObject(ctx, &vectorpb.GoToObjectRequest{ObjectId: id, DistanceFromObjectOriginMm: float32(clamp(a.num("distance", 70), 40, 500)), IdTag: tag})
		return r.GetResult(), err
	})
	actions["dock_with_cube"] = cubeAction(func(ctx context.Context, cli vectorpb.ExternalInterfaceClient, id, tag int32, _ Args) (*vectorpb.ActionResult, error) {
		r, err := cli.DockWithCube(ctx, &vectorpb.DockWithCubeRequest{ObjectId: id, AlignmentType: vectorpb.AlignmentType_ALIGNMENT_TYPE_LIFT_PLATE, IdTag: tag})
		return r.GetResult(), err
	})
	actions["pickup_cube"] = cubeAction(func(ctx context.Context, cli vectorpb.ExternalInterfaceClient, id, tag int32, _ Args) (*vectorpb.ActionResult, error) {
		r, err := cli.PickupObject(ctx, &vectorpb.PickupObjectRequest{ObjectId: id, UsePreDockPose: true, IdTag: tag})
		return r.GetResult(), err
	})
	actions["roll_cube"] = cubeAction(func(ctx context.Context, cli vectorpb.ExternalInterfaceClient, id, tag int32, _ Args) (*vectorpb.ActionResult, error) {
		r, err := cli.RollObject(ctx, &vectorpb.RollObjectRequest{ObjectId: id, UsePreDockPose: true, IdTag: tag})
		return r.GetResult(), err
	})
}

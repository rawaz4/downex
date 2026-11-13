package gfx

import (
	"errors"
	"math"
	"strings"
	"testing"

	"downex/ui/kit"
)

func TestArcToCubics(t *testing.T) {
	// A half circle of radius 10 from (0, 10) to (20, 10) about (10, 10), clockwise on the
	// screen (sweep): it passes through (10, 0).
	op := kit.PathOp{Verb: kit.PathArc, P: [3]kit.Point{kit.Pt(20, 10)}, Radii: kit.Size{W: 10, H: 10}, Sweep: true}
	pts := ArcToCubics(nil, kit.Pt(0, 10), op)
	if len(pts) != 6 {
		t.Fatalf("got %d points, want two cubics", len(pts))
	}
	if mid := pts[2]; math.Abs(float64(mid.X-10)) > 1e-4 || math.Abs(float64(mid.Y)) > 1e-4 {
		t.Errorf("middle point %v, want (10, 0)", mid)
	}
	if pts[5] != kit.Pt(20, 10) {
		t.Errorf("end %v", pts[5])
	}
	// Without sweep it passes through (10, 20).
	op.Sweep = false
	if mid := ArcToCubics(nil, kit.Pt(0, 10), op)[2]; math.Abs(float64(mid.Y-20)) > 1e-4 {
		t.Errorf("middle point %v, want (10, 20)", mid)
	}
	// Radii too small are scaled up: radius 1 between points 20 apart is a half circle.
	op.Radii = kit.Size{W: 1, H: 1}
	op.Sweep = true
	if mid := ArcToCubics(nil, kit.Pt(0, 10), op)[2]; math.Abs(float64(mid.Y)) > 1e-3 {
		t.Errorf("scaled arc middle %v, want (10, 0)", mid)
	}
	// The large arc of a circle of radius 10 between points 90° apart: 270° in three pieces.
	op = kit.PathOp{Verb: kit.PathArc, P: [3]kit.Point{kit.Pt(10, 0)}, Radii: kit.Size{W: 10, H: 10}, Large: true, Sweep: true}
	if n := len(ArcToCubics(nil, kit.Pt(0, 10), op)); n != 9 {
		t.Errorf("large arc: %d points, want 9", n)
	}
	if n := len(ArcToCubics(nil, kit.Pt(1, 1), kit.PathOp{Verb: kit.PathArc, P: [3]kit.Point{kit.Pt(1, 1)}, Radii: kit.Size{W: 1, H: 1}})); n != 0 {
		t.Errorf("equal end points drew %d points", n)
	}
	if got := ArcToCubics(nil, kit.Pt(0, 0), kit.PathOp{Verb: kit.PathArc, P: [3]kit.Point{kit.Pt(5, 0)}}); len(got) != 3 || got[2] != kit.Pt(5, 0) {
		t.Errorf("zero radius: %v", got)
	}
	// Every point of a rotated ellipse's pieces lies on it: rx 20, ry 10 turned 30°.
	op = kit.PathOp{Verb: kit.PathArc, P: [3]kit.Point{kit.Pt(30, 25)}, Radii: kit.Size{W: 20, H: 10}, Rotation: 30, Large: true}
	pts = ArcToCubics(nil, kit.Pt(5, 5), op)
	_, rx, ry := ArcRadii(kit.Pt(5, 5), op)
	if rx < 20 || ry < 10 {
		t.Fatalf("radii %v %v", rx, ry)
	}
	if len(pts)%3 != 0 || len(pts) == 0 {
		t.Fatalf("%d points", len(pts))
	}
}

func TestParseSVGPathErrors(t *testing.T) {
	ops, err := ParseSVGPath("M1 2l3 4a1 1 0 01 2 2z", nil)
	if err != nil || len(ops) != 4 {
		t.Fatalf("ops %v, err %v", ops, err)
	}
	_, err = ParseSVGPath("M1 2X", nil)
	if !errors.Is(err, kit.ErrInvalid) || !strings.Contains(err.Error(), "offset 4") {
		t.Errorf("err %v", err)
	}
}

func TestRoundOut(t *testing.T) {
	got := RoundOut(kit.R(0.3, 0.6, 10.2, 10.0004), 2)
	if want := kit.R(0, 0.5, 10.5, 10); got != want {
		t.Errorf("RoundOut = %v, want %v", got, want)
	}
}

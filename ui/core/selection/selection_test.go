package selection

import (
	"slices"
	"testing"
)

// The cases of ui/frontend/src/lib/selection.test.ts.

var (
	order = []int64{10, 20, 30, 40, 50}
	idx   = Indexer(order)
)

func want(t *testing.T, s *Selection, ids ...int64) {
	t.Helper()
	if got := s.IDs(); !slices.Equal(got, ids) {
		t.Fatalf("ids = %v, want %v", got, ids)
	}
}

func focusIs(t *testing.T, s *Selection, id int64) {
	t.Helper()
	if f, ok := s.Focus(); !ok || f != id {
		t.Fatalf("focus = %d (%v), want %d", f, ok, id)
	}
}

func anchorIs(t *testing.T, s *Selection, id int64) {
	t.Helper()
	if a, ok := s.Anchor(); !ok || a != id {
		t.Fatalf("anchor = %d (%v), want %d", a, ok, id)
	}
}

var (
	toggle = Modifiers{Toggle: true}
	shift  = Modifiers{Shift: true}
	none   = Modifiers{}
)

func TestPointerSelection(t *testing.T) {
	t.Run("plain click selects one row and sets anchor and focus", func(t *testing.T) {
		s := Click(SelectOnly(10), order, idx, 30, none)
		want(t, s, 30)
		anchorIs(t, s, 30)
		focusIs(t, s, 30)
	})
	t.Run("ctrl-click toggles rows", func(t *testing.T) {
		s := Click(Empty, order, idx, 20, none)
		s = Click(s, order, idx, 40, toggle)
		want(t, s, 20, 40)
		s = Click(s, order, idx, 20, toggle)
		want(t, s, 40)
		focusIs(t, s, 20)
	})
	t.Run("shift-click selects the range from the anchor, in either direction", func(t *testing.T) {
		s := Click(Empty, order, idx, 20, none)
		s = Click(s, order, idx, 40, shift)
		want(t, s, 20, 30, 40)
		anchorIs(t, s, 20)
		s = Click(s, order, idx, 10, shift)
		want(t, s, 10, 20)
	})
	t.Run("ctrl+shift-click adds a range to the selection", func(t *testing.T) {
		s := Click(Empty, order, idx, 10, none)
		s = Click(s, order, idx, 30, toggle)
		s = Click(s, order, idx, 50, Modifiers{Shift: true, Toggle: true})
		want(t, s, 10, 30, 40, 50)
	})
	t.Run("shift-click without an anchor selects just the row", func(t *testing.T) {
		want(t, Click(Empty, order, idx, 30, shift), 30)
	})
	t.Run("ignores ids that are not displayed", func(t *testing.T) {
		s := SelectOnly(10)
		if Click(s, order, idx, 999, none) != s {
			t.Fatal("a click on a hidden id changed the selection")
		}
	})
	t.Run("right-click keeps a selection containing the row", func(t *testing.T) {
		multi := All(Empty, order)
		want(t, Context(multi, 30), order...)
		focusIs(t, Context(multi, 30), 30)
		want(t, Context(SelectOnly(10), 30), 30)
	})
}

func TestKeyboardSelection(t *testing.T) {
	t.Run("starts at the first or last row", func(t *testing.T) {
		focusIs(t, MoveFocus(Empty, order, idx, By(1), none), 10)
		focusIs(t, MoveFocus(Empty, order, idx, By(-1), none), 50)
	})
	t.Run("moves and clamps", func(t *testing.T) {
		s := SelectOnly(20)
		s = MoveFocus(s, order, idx, By(1), none)
		want(t, s, 30)
		s = MoveFocus(s, order, idx, By(10), none)
		focusIs(t, s, 50)
		s = MoveFocus(s, order, idx, First, none)
		want(t, s, 10)
		s = MoveFocus(s, order, idx, By(-1), none)
		focusIs(t, s, 10)
	})
	t.Run("shift extends from the anchor and can shrink back", func(t *testing.T) {
		s := SelectOnly(20)
		s = MoveFocus(s, order, idx, By(1), shift)
		s = MoveFocus(s, order, idx, By(1), shift)
		want(t, s, 20, 30, 40)
		s = MoveFocus(s, order, idx, By(-1), shift)
		want(t, s, 20, 30)
		s = MoveFocus(s, order, idx, Last, shift)
		want(t, s, 20, 30, 40, 50)
	})
	t.Run("ctrl moves only the cursor; ctrl+space toggles under it", func(t *testing.T) {
		s := SelectOnly(10)
		s = MoveFocus(s, order, idx, By(2), toggle)
		want(t, s, 10)
		focusIs(t, s, 30)
		s = ToggleFocused(s)
		want(t, s, 10, 30)
		s = ToggleFocused(s)
		want(t, s, 10)
	})
	t.Run("does nothing on an empty list", func(t *testing.T) {
		if MoveFocus(Empty, nil, Indexer(nil), By(1), none) != Empty {
			t.Fatal("moving in an empty list changed the selection")
		}
	})
}

func TestSelectAllPruneOrder(t *testing.T) {
	t.Run("selects every displayed row and keeps the cursor", func(t *testing.T) {
		s := All(SelectOnly(30), order)
		want(t, s, order...)
		focusIs(t, s, 30)
		if All(SelectOnly(30), nil) != Empty {
			t.Fatal("select all of nothing is not the empty selection")
		}
	})
	t.Run("prunes ids that disappeared, returning the same object when nothing changed", func(t *testing.T) {
		s := All(SelectOnly(30), order)
		if Prune(s, func(int64) bool { return true }) != s {
			t.Fatal("prune without a change made a new selection")
		}
		pruned := Prune(s, func(id int64) bool { return id != 30 && id != 40 })
		want(t, pruned, 10, 20, 50)
		if _, ok := pruned.Focus(); ok {
			t.Fatal("the focus of a pruned row stayed")
		}
	})
	t.Run("returns selected ids in displayed order", func(t *testing.T) {
		s := Click(Empty, order, idx, 50, none)
		s = Click(s, order, idx, 10, toggle)
		if got := Ordered(s, order); !slices.Equal(got, []int64{10, 50}) {
			t.Fatalf("ordered = %v", got)
		}
	})
}

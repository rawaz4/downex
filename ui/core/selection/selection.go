// Package selection is the download table's multi-selection model (click, Shift, Ctrl and the
// keyboard), ported from ui/frontend/src/lib/selection.ts. It is pure and immutable: every
// operation returns a Selection, the given one when nothing changed. order is the list of ids
// as currently displayed.
package selection

import "slices"

// Selection is a set of selected download ids with the fixed end of a Shift range (the anchor)
// and the keyboard cursor (the focus). A *Selection is never modified once made, so callers can
// compare pointers to see whether anything changed.
type Selection struct {
	ids       map[int64]struct{}
	anchor    int64
	focus     int64
	hasAnchor bool
	hasFocus  bool
}

// Empty selects nothing and has neither anchor nor focus.
var Empty = &Selection{ids: map[int64]struct{}{}}

// Has reports whether id is selected.
func (s *Selection) Has(id int64) bool {
	_, ok := s.ids[id]
	return ok
}

// Len is the number of selected ids.
func (s *Selection) Len() int { return len(s.ids) }

// IDs returns the selected ids in ascending order (OrderedIDs gives the displayed order).
func (s *Selection) IDs() []int64 {
	out := make([]int64, 0, len(s.ids))
	for id := range s.ids {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// Anchor is the fixed end of a Shift range; ok is false when there is none.
func (s *Selection) Anchor() (id int64, ok bool) { return s.anchor, s.hasAnchor }

// Focus is the keyboard cursor (the active row); ok is false when there is none.
func (s *Selection) Focus() (id int64, ok bool) { return s.focus, s.hasFocus }

// Modifiers are the keys held during a click or a move.
type Modifiers struct {
	Shift bool
	// Toggle is Ctrl on Windows and Linux (Cmd is accepted too).
	Toggle bool
}

// IndexOf looks an id up in the displayed order: its index, or -1 when it is not shown.
type IndexOf func(id int64) int

// Indexer builds the index lookup over the displayed order, once per list change.
func Indexer(order []int64) IndexOf {
	m := make(map[int64]int, len(order))
	for i, id := range order {
		m[id] = i
	}
	return func(id int64) int {
		if i, ok := m[id]; ok {
			return i
		}
		return -1
	}
}

func span(order []int64, from, to int) []int64 {
	lo, hi := min(from, to), max(from, to)
	return order[lo : hi+1]
}

func setOf(ids ...[]int64) map[int64]struct{} {
	n := 0
	for _, l := range ids {
		n += len(l)
	}
	m := make(map[int64]struct{}, n)
	for _, l := range ids {
		for _, id := range l {
			m[id] = struct{}{}
		}
	}
	return m
}

func (s *Selection) copyIDs() map[int64]struct{} {
	m := make(map[int64]struct{}, len(s.ids)+1)
	for id := range s.ids {
		m[id] = struct{}{}
	}
	return m
}

func (s *Selection) list() []int64 {
	out := make([]int64, 0, len(s.ids))
	for id := range s.ids {
		out = append(out, id)
	}
	return out
}

// SelectOnly selects id alone, with anchor and focus on it.
func SelectOnly(id int64) *Selection {
	return &Selection{ids: map[int64]struct{}{id: {}}, anchor: id, focus: id, hasAnchor: true, hasFocus: true}
}

// Click is pointer selection: plain selects one row, Toggle adds or removes it, Shift selects
// the range from the anchor (Toggle+Shift adds the range). An id that is not displayed changes
// nothing.
func Click(sel *Selection, order []int64, indexOf IndexOf, id int64, mods Modifiers) *Selection {
	target := indexOf(id)
	if target < 0 {
		return sel
	}
	if mods.Shift {
		anchor := id
		if sel.hasAnchor && indexOf(sel.anchor) >= 0 {
			anchor = sel.anchor
		}
		r := span(order, indexOf(anchor), target)
		ids := setOf(r)
		if mods.Toggle {
			ids = setOf(sel.list(), r)
		}
		return &Selection{ids: ids, anchor: anchor, focus: id, hasAnchor: true, hasFocus: true}
	}
	if mods.Toggle {
		ids := sel.copyIDs()
		if _, ok := ids[id]; ok {
			delete(ids, id)
		} else {
			ids[id] = struct{}{}
		}
		return &Selection{ids: ids, anchor: id, focus: id, hasAnchor: true, hasFocus: true}
	}
	return SelectOnly(id)
}

// Context is a right-click: it keeps a selection that contains the row (moving the focus to it),
// else selects just the row.
func Context(sel *Selection, id int64) *Selection {
	if sel.Has(id) {
		c := *sel
		c.focus, c.hasFocus = id, true
		return &c
	}
	return SelectOnly(id)
}

type moveKind uint8

const (
	moveBy moveKind = iota
	moveFirst
	moveLast
)

// Move is a keyboard movement: a number of rows, or to the first or last row.
type Move struct {
	kind moveKind
	by   int
}

// By moves n rows (negative: up).
func By(n int) Move { return Move{kind: moveBy, by: n} }

// First moves to the first row; Last to the last one.
var (
	First = Move{kind: moveFirst}
	Last  = Move{kind: moveLast}
)

// MoveFocus is keyboard movement. Plain: move and select the row. Shift: extend from the
// anchor. Toggle (Ctrl): move the cursor only, keeping the selection.
func MoveFocus(sel *Selection, order []int64, indexOf IndexOf, move Move, mods Modifiers) *Selection {
	if len(order) == 0 {
		return sel
	}
	current := -1
	if sel.hasFocus {
		current = indexOf(sel.focus)
	}
	var next int
	switch {
	case move.kind == moveFirst:
		next = 0
	case move.kind == moveLast:
		next = len(order) - 1
	case current < 0:
		if move.by > 0 {
			next = 0
		} else {
			next = len(order) - 1
		}
	default:
		next = min(len(order)-1, max(0, current+move.by))
	}
	id := order[next]
	if mods.Shift {
		var anchor int64
		switch {
		case sel.hasAnchor && indexOf(sel.anchor) >= 0:
			anchor = sel.anchor
		case current >= 0:
			anchor = sel.focus
		default:
			anchor = id
		}
		r := span(order, indexOf(anchor), next)
		ids := setOf(r)
		if mods.Toggle {
			ids = setOf(sel.list(), r)
		}
		return &Selection{ids: ids, anchor: anchor, focus: id, hasAnchor: true, hasFocus: true}
	}
	if mods.Toggle {
		c := *sel
		c.focus, c.hasFocus = id, true
		return &c
	}
	return SelectOnly(id)
}

// ToggleFocused is Ctrl+Space: it toggles the row under the cursor.
func ToggleFocused(sel *Selection) *Selection {
	if !sel.hasFocus {
		return sel
	}
	ids := sel.copyIDs()
	if _, ok := ids[sel.focus]; ok {
		delete(ids, sel.focus)
	} else {
		ids[sel.focus] = struct{}{}
	}
	return &Selection{ids: ids, anchor: sel.focus, focus: sel.focus, hasAnchor: true, hasFocus: true}
}

// All selects every displayed row, keeping the cursor when it is shown (else on the first row).
func All(sel *Selection, order []int64) *Selection {
	if len(order) == 0 {
		return Empty
	}
	focus := order[0]
	if sel.hasFocus && slices.Contains(order, sel.focus) {
		focus = sel.focus
	}
	anchor := focus
	if sel.hasAnchor {
		anchor = sel.anchor
	}
	return &Selection{ids: setOf(order), anchor: anchor, focus: focus, hasAnchor: true, hasFocus: true}
}

// Prune drops ids that are no longer listed. It returns sel itself when nothing changed, so
// consumers are not invalidated needlessly.
func Prune(sel *Selection, has func(id int64) bool) *Selection {
	changed := false
	ids := make(map[int64]struct{}, len(sel.ids))
	for id := range sel.ids {
		if has(id) {
			ids[id] = struct{}{}
		} else {
			changed = true
		}
	}
	hasAnchor := sel.hasAnchor && has(sel.anchor)
	hasFocus := sel.hasFocus && has(sel.focus)
	if !changed && hasAnchor == sel.hasAnchor && hasFocus == sel.hasFocus {
		return sel
	}
	out := &Selection{ids: ids, hasAnchor: hasAnchor, hasFocus: hasFocus}
	if hasAnchor {
		out.anchor = sel.anchor
	}
	if hasFocus {
		out.focus = sel.focus
	}
	return out
}

// Ordered returns the selected ids in displayed order.
func Ordered(sel *Selection, order []int64) []int64 {
	if len(sel.ids) == 0 {
		return nil
	}
	out := make([]int64, 0, len(sel.ids))
	for _, id := range order {
		if sel.Has(id) {
			out = append(out, id)
		}
	}
	return out
}

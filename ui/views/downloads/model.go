package downloads

import (
	"slices"

	"downex/ui/core/selection"
	"downex/ui/kit"
	"downex/ui/state"
)

// listModel is the rows the grid shows: a snapshot of the store's visible ids, taken when
// the store changes, so that the list sees one order between its RowsChanged calls.
type listModel struct {
	ids   []int64
	index map[int64]int
}

func newListModel() *listModel { return &listModel{index: map[int64]int{}} }

func (m *listModel) Len() int { return len(m.ids) }

func (m *listModel) Key(i int) kit.ListKey { return kit.ListKey(m.ids[i]) }

func (m *listModel) Index(k kit.ListKey) (int, bool) {
	i, ok := m.index[int64(k)]
	return i, ok
}

// set takes a new order and reports whether it differs from the one held. The store hands
// out a new slice for every revision and never changes one it gave, so it is kept as it is.
func (m *listModel) set(ids []int64) bool {
	if slices.Equal(ids, m.ids) {
		m.ids = ids
		return false
	}
	m.ids = ids
	clear(m.index)
	for i, id := range ids {
		m.index[id] = i
	}
	return true
}

// listSelection is the kit's view of the store's selection (lib/selection.ts's rules are the
// store's): the list calls it on clicks and keys.
type listSelection struct {
	store *state.DownloadsStore
	// clicked hears every pointer selection (the table leaves the header row).
	clicked func()
}

func (s *listSelection) Selected(k kit.ListKey) bool { return s.store.Selection().Has(int64(k)) }

func (s *listSelection) Cursor() (kit.ListKey, bool) {
	id, ok := s.store.Selection().Focus()
	return kit.ListKey(id), ok
}

func (s *listSelection) SetCursor(k kit.ListKey) { s.store.SetCursor(int64(k)) }

func mods(m kit.SelectMods) selection.Modifiers {
	return selection.Modifiers{Shift: m.Shift, Toggle: m.Toggle}
}

func (s *listSelection) Click(k kit.ListKey, m kit.SelectMods) {
	if s.clicked != nil {
		s.clicked()
	}
	s.store.Click(int64(k), mods(m))
}

func (s *listSelection) ContextClick(k kit.ListKey) {
	if s.clicked != nil {
		s.clicked()
	}
	s.store.ContextClick(int64(k))
}

func (s *listSelection) Move(by kit.ListMove, m kit.SelectMods) {
	mv := selection.By(int(by))
	switch by {
	case kit.MoveFirst:
		mv = selection.First
	case kit.MoveLast:
		mv = selection.Last
	}
	s.store.Move(mv, mods(m))
}

func (s *listSelection) ToggleCursor() { s.store.ToggleFocused() }

func (s *listSelection) AppendSelected(dst []kit.ListKey) []kit.ListKey {
	for _, id := range s.store.Selection().IDs() {
		dst = append(dst, kit.ListKey(id))
	}
	return dst
}

// arrivals tracks which ids appear and disappear between readings of the list (motion.svelte.ts
// Arrivals): the first reading once the list is ready only records what is there.
type arrivals struct {
	known, next map[int64]bool
	seeded      bool
	added       []int64
	removed     []int64
}

func newArrivals() *arrivals { return &arrivals{known: map[int64]bool{}, next: map[int64]bool{}} }

// update reads the ids now; added and removed hold the changes until the next call.
func (a *arrivals) update(all []*state.Download, ready bool) {
	a.added, a.removed = a.added[:0], a.removed[:0]
	if !ready {
		return
	}
	clear(a.next)
	for _, d := range all {
		id := d.GetId()
		a.next[id] = true
		if a.seeded && !a.known[id] {
			a.added = append(a.added, id)
		}
	}
	if a.seeded {
		for id := range a.known {
			if !a.next[id] {
				a.removed = append(a.removed, id)
			}
		}
		slices.Sort(a.removed)
	}
	a.known, a.next = a.next, a.known
	a.seeded = true
}

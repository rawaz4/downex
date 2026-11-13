//go:build windows

package comobj

// Handle names one entry of a Table: its slot and the slot's generation. It is never 0,
// and a handle whose entry was removed never names a later entry of the same slot.
type Handle uint64

// Table holds the Go side of objects that COM holds, by Handle. It is not synchronised:
// its owner serialises the calls.
type Table[T any] struct {
	slots []slot[T]
	free  []uint32
	live  int
}

type slot[T any] struct {
	gen  uint32
	used bool
	v    T
}

// Add stores v and returns its handle.
func (t *Table[T]) Add(v T) Handle {
	var i uint32
	if n := len(t.free); n > 0 {
		i = t.free[n-1]
		t.free = t.free[:n-1]
	} else {
		t.slots = append(t.slots, slot[T]{})
		i = uint32(len(t.slots) - 1)
	}
	s := &t.slots[i]
	s.gen++
	if s.gen == 0 {
		s.gen = 1
	}
	s.used, s.v = true, v
	t.live++
	return Handle(uint64(s.gen)<<32 | uint64(i+1))
}

func (t *Table[T]) at(h Handle) *slot[T] {
	i := uint32(h) - 1
	if h == 0 || uint64(i) >= uint64(len(t.slots)) {
		return nil
	}
	s := &t.slots[i]
	if !s.used || s.gen != uint32(h>>32) {
		return nil
	}
	return s
}

// Get is the value of h, or false when h names no entry.
func (t *Table[T]) Get(h Handle) (T, bool) {
	if s := t.at(h); s != nil {
		return s.v, true
	}
	var zero T
	return zero, false
}

// Remove removes h's entry and reports whether there was one.
func (t *Table[T]) Remove(h Handle) bool {
	s := t.at(h)
	if s == nil {
		return false
	}
	var zero T
	s.used, s.v = false, zero
	t.free = append(t.free, uint32(h)-1)
	t.live--
	return true
}

// Len is the number of entries.
func (t *Table[T]) Len() int { return t.live }

package engine

import (
	"fmt"
	"sync"

	"downex/internal/model"
)

// runCtl carries live changes from Engine methods to the coordinator of a running
// download. It exists while Run (or Restart, which accepts no changes) holds the
// download.
type runCtl struct {
	runs bool          // held by Run, not by Restart
	wake chan struct{} // capacity 1: a change is pending

	mu      sync.Mutex
	maxConn int  // requested connection limit (0 = engine default)
	pending bool // maxConn has not been applied yet
}

func newRunCtl(runs bool) *runCtl { return &runCtl{runs: runs, wake: make(chan struct{}, 1)} }

func (r *runCtl) setMaxConnections(n int) {
	r.mu.Lock()
	r.maxConn, r.pending = n, true
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// takeMaxConnections returns the pending connection limit, if any, and clears it.
func (r *runCtl) takeMaxConnections() (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.maxConn, r.pending
	r.pending = false
	return n, ok
}

// SetMaxConnections changes the connection limit of a running download (n ≤ 0
// selects the engine default; values above the hard cap are clamped). Lowering it
// stops the connections in the highest slots at once: every byte they reserved is
// still written, and the unreserved rest of their ranges returns to the pool for
// the remaining connections. Raising it lets ramp-up add connections up to the new
// limit. The new value is part of the record the run commits.
//
// For a download this Engine is not running it returns an error wrapping
// ErrNotRunning and changes nothing; the caller stores the value for the next Run.
func (e *Engine) SetMaxConnections(id model.DownloadID, n int) error {
	n = min(max(n, 0), e.opts.HardCap)
	e.mu.Lock()
	ctl := e.running[id]
	e.mu.Unlock()
	if ctl == nil || !ctl.runs {
		return fmt.Errorf("%w: %d", ErrNotRunning, id)
	}
	ctl.setMaxConnections(n)
	return nil
}

// control returns the live-change channel of a download this Engine runs, or nil.
func (e *Engine) control(id model.DownloadID) *runCtl {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running[id]
}

// setMaxConnections applies a new connection limit (§3). Lowering it below the live
// connections cancels the workers in the highest slots: a worker reserves bytes
// under its segment's lock before it queues them for the writer, so cutting the
// segment at its reserved position loses nothing; the unreserved tail becomes an
// unowned segment that the remaining workers take when they finish theirs.
func (c *coordinator) setMaxConnections(n int) {
	if c.d.MaxConnections != n {
		c.d.MaxConnections = n
		c.dirty = true // committed with the next checkpoint
	}
	if c.stopping {
		return
	}
	limit := c.perHostMax()
	c.ramp.target = min(c.ramp.target, limit) // raising ramps up again from here
	for i := len(c.rows) - 1; i >= 0 && c.live > limit; i-- {
		w := c.rows[i].w
		if w == nil || w.retired {
			continue
		}
		if s := w.seg; s != nil {
			if newStart, oldEnd, split := s.detach(w); split {
				c.insertAfter(s, newSegment(newStart, oldEnd, 0, nil))
			}
			w.seg = nil
		}
		c.cancelWorker(w)
	}
	c.log.Debug("connection limit changed", "max", n, "limit", limit, "live", c.live)
}

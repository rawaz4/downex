package scene

import (
	"context"
	"sync"

	downexv1 "downex/internal/rpc/downex/v1"
)

// stream is a Watch stream of the simulated daemon.
type stream struct {
	mu     sync.Mutex
	cond   *sync.Cond
	queue  []*downexv1.WatchResponse
	msg    *downexv1.WatchResponse
	closed bool
	err    error
	unsubs []func()
}

func newStream(ctx context.Context) *stream {
	s := &stream{}
	s.cond = sync.NewCond(&s.mu)
	stop := context.AfterFunc(ctx, func() { s.end(ctx.Err()) })
	s.unsubs = append(s.unsubs, func() { stop() })
	return s
}

func (s *stream) onClose(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unsubs = append(s.unsubs, f)
}

func (s *stream) push(m *downexv1.WatchResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.queue = append(s.queue, m)
		s.cond.Broadcast()
	}
}

func (s *stream) end(err error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed, s.err = true, err
	unsubs := s.unsubs
	s.unsubs = nil
	s.cond.Broadcast()
	s.mu.Unlock()
	for _, f := range unsubs {
		f()
	}
}

func (s *stream) Receive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.queue) == 0 && !s.closed {
		s.cond.Wait()
	}
	if len(s.queue) == 0 {
		return false
	}
	s.msg = s.queue[0]
	s.queue = s.queue[1:]
	return true
}

func (s *stream) Msg() *downexv1.WatchResponse { s.mu.Lock(); defer s.mu.Unlock(); return s.msg }

func (s *stream) Err() error { s.mu.Lock(); defer s.mu.Unlock(); return s.err }

func (s *stream) Close() error {
	s.end(nil)
	return nil
}

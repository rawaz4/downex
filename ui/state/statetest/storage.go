package statetest

import (
	"errors"
	"sync"
)

// Memory is an in-memory state.SharedStorage (one window's localStorage); OtherWindowWrites
// changes it as another window would.
type Memory struct {
	mu   sync.Mutex
	data map[string]string
	subs map[int]func(string)
	next int
}

// NewMemory holds initial.
func NewMemory(initial map[string]string) *Memory {
	m := &Memory{data: map[string]string{}, subs: map[int]func(string){}}
	for k, v := range initial {
		m.data[k] = v
	}
	return m
}

// Get implements state.Storage.
func (m *Memory) Get(key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	return v, ok, nil
}

// Set implements state.Storage.
func (m *Memory) Set(key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	return nil
}

// Remove implements state.Storage.
func (m *Memory) Remove(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

// Value is key's value (false when unset).
func (m *Memory) Value(key string) (string, bool) { v, ok, _ := m.Get(key); return v, ok }

// Watch implements state.SharedStorage.
func (m *Memory) Watch(f func(string)) (cancel func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.next
	m.next++
	m.subs[id] = f
	return func() { m.mu.Lock(); delete(m.subs, id); m.mu.Unlock() }
}

// OtherWindowWrites stores value under key as another window would ("" removes it) and,
// with notify, tells the watchers (its storage event; without, an event that did not
// arrive).
func (m *Memory) OtherWindowWrites(key, value string, notify bool) {
	m.mu.Lock()
	if value == "" {
		delete(m.data, key)
	} else {
		m.data[key] = value
	}
	var subs []func(string)
	if notify {
		for _, f := range m.subs {
			subs = append(subs, f)
		}
	}
	m.mu.Unlock()
	for _, f := range subs {
		f(key)
	}
}

// Broken is a storage that always fails (storage disabled, quota exceeded).
type Broken struct{}

var errBroken = errors.New("SecurityError: storage is disabled")

// Get implements state.Storage.
func (Broken) Get(string) (string, bool, error) { return "", false, errBroken }

// Set implements state.Storage.
func (Broken) Set(string, string) error { return errors.New("QuotaExceededError") }

// Remove implements state.Storage.
func (Broken) Remove(string) error { return errBroken }

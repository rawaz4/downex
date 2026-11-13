package daemon

import (
	"context"
	"testing"
	"time"
)

// TestRuntimeUpdateDoesNotCloseUnderLock: releasing the previous per-URL system
// resolver (closing a WinHTTP session waits for a WPAD lookup in progress) never blocks
// the readers of the runtime: host rules, proxy resolution and logins stay available
// while the old resolver closes.
func TestRuntimeUpdateDoesNotCloseUnderLock(t *testing.T) {
	for _, step := range []struct {
		name string
		run  func(rt *NetworkRuntime)
	}{
		{"update", func(rt *NetworkRuntime) {
			_ = rt.Update(context.Background(), NetworkSettings{Proxy: ProxySettings{Mode: ProxyModeNone}})
		}},
		{"close", func(rt *NetworkRuntime) { rt.Close() }},
	} {
		t.Run(step.name, func(t *testing.T) {
			rt := NewNetworkRuntime(nil, nil)
			if err := rt.Update(context.Background(), NetworkSettings{
				Proxy:     ProxySettings{Mode: ProxyModeNone},
				HostRules: []HostRule{{Pattern: "files.test", MaxConnections: 3}},
			}); err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			closed := make(chan struct{})
			rt.mu.Lock()
			rt.autoCloser = func() {
				close(entered)
				<-release
				close(closed)
			}
			rt.mu.Unlock()
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})

			go step.run(rt)
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("the old resolver was never closed")
			}
			readers := make(chan struct{})
			go func() {
				defer close(readers)
				rt.HostMaxConnections("files.test")
				_, _ = rt.ResolveProxy(context.Background(), mustURLN(t, "http://files.test/"))
				rt.ServerLogin(mustURLN(t, "http://files.test/"))
			}()
			select {
			case <-readers:
			case <-time.After(2 * time.Second):
				t.Fatal("readers of the runtime blocked while the old resolver closes")
			}
			close(release)
			select {
			case <-closed:
			case <-time.After(10 * time.Second):
				t.Fatal("the closer did not finish")
			}
		})
	}
}

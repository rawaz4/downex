package daemon

import (
	"context"
	"os"
	"testing"
	"time"

	"downex/internal/pac"
)

// TestMain lets the test binary double as the isolated PAC evaluator's helper process
// (pac.ServeIfHelper), as downexd does.
func TestMain(m *testing.M) {
	pac.ServeIfHelper()
	os.Exit(m.Run())
}

// TestRuntimePACHostileScript: a PAC script whose evaluation exhausts memory in native
// code (in the daemon's own process a fatal "out of memory" no recover catches) only
// costs the helper process: connections go direct, the daemon goes on, and a later
// configuration is evaluated again.
func TestRuntimePACHostileScript(t *testing.T) {
	rt := NewNetworkRuntime(nil, nil)
	t.Cleanup(rt.Close)
	script := `function FindProxyForURL(u, h) { return "x".repeat(Math.pow(2, 40)); }`
	rt.fetch = func(context.Context, string) (string, error) { return script, nil }
	ctx := context.Background()
	if err := rt.Update(ctx, NetworkSettings{Proxy: ProxySettings{Mode: ProxyModePAC, PACURL: "http://wpad.test/proxy.pac"}}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	start := time.Now()
	routes, err := rt.ResolveProxy(ctx, mustURLN(t, "http://example.test/file.zip"))
	if err != nil || len(routes) != 0 {
		t.Fatalf("ResolveProxy = %v, %v; want direct", routes, err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("ResolveProxy took %v", elapsed)
	}

	script = `function FindProxyForURL(u, h) { return "PROXY pac.proxy.test:3128"; }`
	if err := rt.Update(ctx, NetworkSettings{Proxy: ProxySettings{Mode: ProxyModePAC, PACURL: "http://wpad.test/proxy.pac"}}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	routes, err = rt.ResolveProxy(ctx, mustURLN(t, "http://example.test/file.zip"))
	if err != nil || len(routes) != 1 || routes[0].Addr != "pac.proxy.test:3128" {
		t.Fatalf("ResolveProxy after a new script = %v, %v", routes, err)
	}
}

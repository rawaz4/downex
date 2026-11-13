package settings_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"downex/ui/core/validate"
	"downex/ui/kit"
	"downex/ui/views"
)

// syncBuffer is a log sink the test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// semanticDump is everything the window's semantic tree (what UI Automation reads) holds.
func semanticDump(w *kit.Window) string {
	tree := w.Semantics()
	var sb strings.Builder
	var visit func(id kit.NodeID)
	visit = func(id kit.NodeID) {
		n, ok := tree.Node(id)
		if !ok {
			return
		}
		fmt.Fprintf(&sb, "%+v\n", n)
		for _, c := range tree.AppendChildren(nil, id) {
			visit(c)
		}
	}
	visit(tree.Root())
	return sb.String()
}

// Passwords are never shown, logged or exposed to assistive technology (M6-SPEC §2): the ones
// typed into the proxy's and a site login's password fields reach downexd only through
// SetSecret, after the save.
func TestSecretsAreNeverShownOrLogged(t *testing.T) {
	const proxySecret, loginSecret = "Proxy-S3cret-9417", "Login-S3cret-2203"
	logs := &syncBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	r := setup(t, m6(), views.SettingsRequest{Page: validate.PageNetwork}, func(r *rig) {
		r.h.App.Log = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	r.typeIn("Proxy password", proxySecret)
	r.press("Add site login")
	r.typeIn("Address", "https://secure.example.net/*")
	r.typeIn("User name", "carol")
	r.typeIn("Password", loginSecret)

	check := func(when string) {
		t.Helper()
		for _, s := range allStrings(r.root()) {
			if strings.Contains(s, proxySecret) || strings.Contains(s, loginSecret) {
				t.Errorf("%s: a password is shown or named: %q", when, s)
			}
		}
		if dump := semanticDump(r.h.Window); strings.Contains(dump, proxySecret) || strings.Contains(dump, loginSecret) {
			t.Errorf("%s: a password reaches the semantic tree", when)
		}
		for _, p := range r.h.Host.Open() {
			w, ok := p.Root.(*kit.Window)
			if !ok {
				continue
			}
			if dump := semanticDump(w); strings.Contains(dump, proxySecret) || strings.Contains(dump, loginSecret) {
				t.Errorf("%s: a password reaches a popup's semantic tree", when)
			}
		}
		if l := logs.String(); strings.Contains(l, proxySecret) || strings.Contains(l, loginSecret) {
			t.Errorf("%s: a password is logged", when)
		}
	}
	check("typed")
	if dump := semanticDump(r.h.Window); !strings.Contains(dump, "Proxy password") || !strings.Contains(dump, "User name") {
		t.Fatal("the semantic tree does not hold the password fields: the check above saw nothing")
	}
	// the password fields are password fields: the system's mark, nothing for assistive technology
	for _, name := range []string{"Proxy password", "Password"} {
		in := r.input(name)
		if !in.InputOptions().Password {
			t.Errorf("%s is not a password field", name)
		}
	}
	// a page change and back keeps them out of view too
	r.click("general")
	r.click("network")
	check("after a page change")

	// a failed SetSecret says what failed, never the password
	r.dm.failSecret = fmt.Errorf("the store refused %s", "it")
	r.save()
	check("after the save")
	var sent []string
	for _, c := range r.dm.secretCalls() {
		sent = append(sent, c.call+" "+c.kind)
	}
	if strings.Join(sent, ",") == "" {
		t.Fatal("no secret stored")
	}
	for _, s := range r.dm.saved() {
		if b := fmt.Sprint(s); strings.Contains(b, proxySecret) || strings.Contains(b, loginSecret) {
			t.Error("a password was sent in the settings")
		}
	}
}

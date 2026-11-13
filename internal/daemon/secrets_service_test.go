package daemon

import (
	"sync"
	"testing"

	"connectrpc.com/connect"

	"downex/internal/secrets"

	downexv1 "downex/internal/rpc/downex/v1"
)

// memKeyring is an in-memory Keyring for tests (test-scoped names only).
type memKeyring struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newMemKeyring() *memKeyring { return &memKeyring{m: map[string][]byte{}} }

func (k *memKeyring) Set(name string, secret []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[name] = append([]byte(nil), secret...)
	return nil
}

func (k *memKeyring) Get(name string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.m[name]
	if !ok {
		return nil, secrets.ErrNotFound
	}
	return v, nil
}

func (k *memKeyring) Delete(name string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, name)
	return nil
}

func (k *memKeyring) has(name string) bool {
	_, err := k.Get(name)
	return err == nil
}

// withLogin adds a site login for pattern to the harness's settings and returns its
// assigned id.
func (h *harness) withLogin(pattern, user string) string {
	h.t.Helper()
	s := protoSettings(h.m.Settings())
	s.SiteLogins = append(s.SiteLogins, &downexv1.SiteLogin{UrlPattern: pattern, Username: user})
	res, err := h.dm.UpdateSettings(ctxT(h.t), connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: s}))
	if err != nil {
		h.t.Fatalf("update settings: %v", err)
	}
	for _, l := range res.Msg.GetSettings().GetSiteLogins() {
		if l.GetUrlPattern() == pattern && l.GetUsername() == user {
			if l.GetId() == "" {
				h.t.Fatal("the new site login got no id")
			}
			return l.GetId()
		}
	}
	h.t.Fatalf("site login %s is not in the settings", pattern)
	return ""
}

func TestSetAndClearSecret(t *testing.T) {
	k := newMemKeyring()
	h := newHarness(t, harnessOptions{network: NewNetworkRuntime(k, nil)})
	id := h.withLogin("https://files.example.test/*", "alice")

	_, err := h.dm.SetSecret(ctxT(t), connect.NewRequest(&downexv1.SetSecretRequest{
		Kind: SecretKindSiteLogin, Id: id, Password: "top-secret",
	}))
	if err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	if got, _ := k.Get(secretName(SecretKindSiteLogin, id)); string(got) != "top-secret" {
		t.Fatalf("stored secret = %q", got)
	}
	// Settings report that a password is stored, never the password.
	res, err := h.dm.GetSettings(ctxT(t), connect.NewRequest(&downexv1.GetSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	logins := res.Msg.GetSettings().GetSiteLogins()
	if len(logins) != 1 || !logins[0].GetHasPassword() || res.Msg.GetSettings().GetProxy().GetHasPassword() {
		t.Fatalf("site logins %v, proxy %v: want only the login's has_password", logins, res.Msg.GetSettings().GetProxy())
	}

	_, err = h.dm.ClearSecret(ctxT(t), connect.NewRequest(&downexv1.ClearSecretRequest{
		Kind: SecretKindSiteLogin, Id: id,
	}))
	if err != nil {
		t.Fatalf("ClearSecret: %v", err)
	}
	if k.has(secretName(SecretKindSiteLogin, id)) {
		t.Fatal("secret was not cleared")
	}
}

// TestRemovedLoginLosesPassword: a site login removed from the settings takes its
// stored password with it.
func TestRemovedLoginLosesPassword(t *testing.T) {
	k := newMemKeyring()
	h := newHarness(t, harnessOptions{network: NewNetworkRuntime(k, nil)})
	id := h.withLogin("*.example.test", "bob")
	if _, err := h.dm.SetSecret(ctxT(t), connect.NewRequest(&downexv1.SetSecretRequest{
		Kind: SecretKindSiteLogin, Id: id, Password: "pw",
	})); err != nil {
		t.Fatal(err)
	}
	s := protoSettings(h.m.Settings())
	s.SiteLogins = nil
	if _, err := h.dm.UpdateSettings(ctxT(t), connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: s})); err != nil {
		t.Fatal(err)
	}
	if k.has(secretName(SecretKindSiteLogin, id)) {
		t.Fatal("the password of the removed site login is still stored")
	}
}

func TestSetSecretValidation(t *testing.T) {
	h := newHarness(t, harnessOptions{network: NewNetworkRuntime(newMemKeyring(), nil)})
	id := h.withLogin("https://a.example.test/*", "")

	bad := []struct {
		req  *downexv1.SetSecretRequest
		code connect.Code
	}{
		{&downexv1.SetSecretRequest{Kind: "bogus", Password: "x"}, connect.CodeInvalidArgument},
		{&downexv1.SetSecretRequest{Kind: SecretKindSiteLogin, Id: "", Password: "x"}, connect.CodeInvalidArgument},
		{&downexv1.SetSecretRequest{Kind: SecretKindSiteLogin, Id: "a b", Password: "x"}, connect.CodeInvalidArgument},
		{&downexv1.SetSecretRequest{Kind: SecretKindSiteLogin, Id: "unknown", Password: "x"}, connect.CodeNotFound},
		{&downexv1.SetSecretRequest{Kind: SecretKindProxy, Id: "nope", Password: "x"}, connect.CodeInvalidArgument},
		{&downexv1.SetSecretRequest{Kind: SecretKindProxy, Password: ""}, connect.CodeInvalidArgument},
		{&downexv1.SetSecretRequest{Kind: SecretKindSiteLogin, Id: id, Password: ""}, connect.CodeInvalidArgument},
	}
	for i, c := range bad {
		_, err := h.dm.SetSecret(ctxT(t), connect.NewRequest(c.req))
		if connect.CodeOf(err) != c.code {
			t.Errorf("case %d: SetSecret = %v, want %v", i, err, c.code)
		}
	}
	if _, err := h.dm.SetSecret(ctxT(t), connect.NewRequest(&downexv1.SetSecretRequest{
		Kind: SecretKindProxy, Password: "proxy-pw",
	})); err != nil {
		t.Errorf("proxy password: %v", err)
	}
}

func TestSetSecretUnconfigured(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	_, err := h.dm.SetSecret(ctxT(t), connect.NewRequest(&downexv1.SetSecretRequest{
		Kind: SecretKindProxy, Password: "x",
	}))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("without Config.Network, want CodeUnimplemented, got %v", err)
	}
}

package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"downex/internal/update"
)

func runTool(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut, func(k string) string { return env[k] })
	return code, out.String(), errOut.String()
}

func TestKeygenSignVerify(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "update.key")
	code, out, errOut := runTool(t, nil, "keygen", "-out", keyFile)
	if code != 0 {
		t.Fatalf("keygen: %d %s", code, errOut)
	}
	pub := strings.TrimSpace(out)
	if _, err := update.ParsePublicKey(pub); err != nil {
		t.Fatalf("keygen printed %q: %v", out, err)
	}
	if fi, err := os.Stat(keyFile); err != nil || fi.Size() == 0 {
		t.Fatalf("key file: %v", err)
	}
	// Never overwrites a key.
	if code, _, _ := runTool(t, nil, "keygen", "-out", keyFile); code != 1 {
		t.Errorf("keygen over an existing key: exit %d, want 1", code)
	}

	pkg := filepath.Join(dir, "Downex-Setup-1.0.0.exe")
	if err := os.WriteFile(pkg, []byte("setup"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = runTool(t, nil, "asset", "-os", "windows", "-arch", "amd64", "-url", "https://example.com/Downex-Setup-1.0.0.exe", pkg)
	if code != 0 {
		t.Fatalf("asset: %d %s", code, errOut)
	}
	var a update.Asset
	if err := json.Unmarshal([]byte(out), &a); err != nil || a.Size != 5 || len(a.SHA256) != 64 {
		t.Fatalf("asset printed %s (%v)", out, err)
	}
	if code, _, _ := runTool(t, nil, "asset", "-os", "windows", "-arch", "amd64", "-url", "http://example.com/x", pkg); code != 1 {
		t.Errorf("asset with an http URL: exit %d, want 1", code)
	}

	manifest := filepath.Join(dir, "feed.json")
	data, _ := json.Marshal(update.Manifest{Version: "1.0.0", Notes: "First.", Assets: []update.Asset{a}})
	if err := os.WriteFile(manifest, data, 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = runTool(t, nil, "sign", "-key", keyFile, manifest)
	if code != 0 || !strings.Contains(out, pub) {
		t.Fatalf("sign: %d %s %s", code, out, errOut)
	}
	sig, err := os.ReadFile(manifest + ".sig")
	if err != nil {
		t.Fatal(err)
	}
	key, _ := update.ParsePublicKey(pub)
	if err := update.Verify(key, data, sig); err != nil {
		t.Fatalf("the daemon's check of the signature: %v", err)
	}
	if code, out, errOut := runTool(t, nil, "verify", "-pub", pub, manifest); code != 0 || !strings.Contains(out, "OK") {
		t.Errorf("verify: %d %s %s", code, out, errOut)
	}

	// A tampered manifest, or another key, fails.
	if err := os.WriteFile(manifest, bytes.Replace(data, []byte("First."), []byte("Fir5t."), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runTool(t, nil, "verify", "-pub", pub, manifest); code != 1 {
		t.Errorf("verify of a tampered manifest: exit %d, want 1", code)
	}
	if err := os.WriteFile(manifest, data, 0o644); err != nil {
		t.Fatal(err)
	}
	other, _, _ := ed25519.GenerateKey(nil)
	if code, _, _ := runTool(t, nil, "verify", "-pub", base64.StdEncoding.EncodeToString(other), manifest); code != 1 {
		t.Errorf("verify with another key: exit %d, want 1", code)
	}

	// The key from the environment; an invalid manifest is not signed.
	seed, _ := os.ReadFile(keyFile)
	env := map[string]string{envPrivateKey: string(seed)}
	sigOut := filepath.Join(dir, "other.sig")
	if code, _, errOut := runTool(t, env, "sign", "-out", sigOut, manifest); code != 0 {
		t.Errorf("sign with $%s: %d %s", envPrivateKey, code, errOut)
	}
	if code, _, _ := runTool(t, nil, "verify", "-pub", pub, "-sig", sigOut, manifest); code != 0 {
		t.Errorf("verify -sig: exit %d", code)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"version":"1.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runTool(t, env, "sign", bad); code != 1 {
		t.Errorf("sign of an invalid manifest: exit %d, want 1", code)
	}
	if _, err := os.Stat(bad + ".sig"); err == nil {
		t.Error("an invalid manifest was signed")
	}
	if code, _, _ := runTool(t, nil, "sign", manifest); code != 2 {
		t.Errorf("sign without a key: exit %d, want 2", code)
	}
}

func TestKeygenRefusesRepository(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module downex\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(repo, "scripts", "update.key")
	code, _, errOut := runTool(t, nil, "keygen", "-out", key)
	if code != 1 || !strings.Contains(errOut, "repository") {
		t.Errorf("keygen inside the repository: %d %s", code, errOut)
	}
	if _, err := os.Stat(key); err == nil {
		t.Error("a key was written into the repository")
	}
	// Another module is fine.
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "go.mod"), []byte("module example.com/downexfork\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runTool(t, nil, "keygen", "-out", filepath.Join(other, "k")); code != 0 {
		t.Errorf("keygen in another module: %d %s", code, errOut)
	}
}

func TestParsePrivateKey(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	for _, text := range []string{base64.StdEncoding.EncodeToString(priv.Seed()), base64.StdEncoding.EncodeToString(priv) + "\n"} {
		k, err := parsePrivateKey(text)
		if err != nil || !bytes.Equal(k, priv) {
			t.Errorf("parsePrivateKey: %v", err)
		}
	}
	broken := bytes.Clone(priv)
	broken[40] ^= 1
	for _, text := range []string{"", "!!", base64.StdEncoding.EncodeToString([]byte("short")), base64.StdEncoding.EncodeToString(broken)} {
		if _, err := parsePrivateKey(text); err == nil {
			t.Errorf("parsePrivateKey(%q) accepted", text)
		}
	}
	if code, _, _ := runTool(t, nil); code != 2 {
		t.Error("no command: want exit 2")
	}
	if code, _, _ := runTool(t, nil, "frobnicate"); code != 2 {
		t.Error("unknown command: want exit 2")
	}
}

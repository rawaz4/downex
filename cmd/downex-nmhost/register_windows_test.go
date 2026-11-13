//go:build windows

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// TestRegisterRealRegistry registers and unregisters through the real HKCU registry,
// below a private key that is removed afterwards.
func TestRegisterRealRegistry(t *testing.T) {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	prefix := `Software\DownexTest-` + hex.EncodeToString(b)
	t.Cleanup(func() { deleteTree(t, prefix) })

	reg := winRegistry{key: registry.CURRENT_USER, name: "HKCU"}
	inst := installation{
		scope:    "the current user (test)",
		targets:  windowsTargets(filepath.Join(t.TempDir(), "nmhost"), prefix+`\`),
		reg:      reg,
		samePath: windowsSamePath,
	}
	o := testOptions(t, "chrome", "firefox")
	var out bytes.Buffer
	if err := register(inst, o, &out); err != nil {
		t.Fatal(err)
	}
	for _, br := range o.browsers {
		tg := inst.targets[br]
		k, err := registry.OpenKey(registry.CURRENT_USER, tg.regKey, registry.QUERY_VALUE)
		if err != nil {
			t.Fatalf("%s key: %v", br, err)
		}
		v, _, err := k.GetStringValue("")
		k.Close()
		if err != nil || v != tg.file {
			t.Fatalf("%s default value %q %v, want %q", br, v, err, tg.file)
		}
		if m := readManifest(t, tg.file); m.Path != o.exe {
			t.Fatalf("%s manifest path %q", br, m.Path)
		}
	}
	// The key paths really are the browsers' keys below the private prefix.
	if got := inst.targets["chrome"].regKey; got != prefix+`\Software\Google\Chrome\NativeMessagingHosts\com.downex.host` {
		t.Fatalf("chrome key %s", got)
	}

	out.Reset()
	if err := register(inst, o, &out); err != nil || !bytes.Contains(out.Bytes(), []byte("Nothing to change")) {
		t.Fatalf("second register: %v\n%s", err, out.String())
	}

	// A value that differs is backed up, and restored by unregister.
	ff := inst.targets["firefox"]
	if err := reg.setValue(ff.regKey, "", `C:\Other\host.json`); err != nil {
		t.Fatal(err)
	}
	if err := register(inst, o, &out); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := reg.value(ff.regKey, backupValueName); err != nil || !ok || v != `C:\Other\host.json` {
		t.Fatalf("backup value %q %v %v", v, ok, err)
	}

	out.Reset()
	if err := unregister(inst, o.browsers, false, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.OpenKey(registry.CURRENT_USER, inst.targets["chrome"].regKey, registry.QUERY_VALUE); !errors.Is(err, registry.ErrNotExist) {
		t.Fatalf("chrome key still exists: %v\n%s", err, out.String())
	}
	if v, ok, _ := reg.value(ff.regKey, ""); !ok || v != `C:\Other\host.json` {
		t.Fatalf("firefox value not restored: %q %v", v, ok)
	}
	if _, ok, _ := reg.value(ff.regKey, backupValueName); ok {
		t.Fatal("backup value left behind")
	}
	if exists(inst.targets["chrome"].file) || exists(ff.file) {
		t.Fatal("manifests left behind")
	}
}

// deleteTree removes key and its subkeys from HKCU.
func deleteTree(t *testing.T, key string) {
	k, err := registry.OpenKey(registry.CURRENT_USER, key, registry.ENUMERATE_SUB_KEYS)
	if errors.Is(err, registry.ErrNotExist) {
		return
	}
	if err != nil {
		t.Errorf("open %s: %v", key, err)
		return
	}
	subs, err := k.ReadSubKeyNames(-1)
	k.Close()
	if err != nil {
		t.Errorf("list %s: %v", key, err)
	}
	for _, s := range subs {
		deleteTree(t, key+`\`+s)
	}
	if err := registry.DeleteKey(registry.CURRENT_USER, key); err != nil && !errors.Is(err, registry.ErrNotExist) {
		t.Errorf("delete %s: %v", key, err)
	}
}

// systemTestInstallation is a system-wide installation below dirs (the last holds the
// manifests) with an in-memory registry.
func systemTestInstallation(dirs []string, reg *memRegistry, md manifestDirs) installation {
	md.dirs = dirs
	return installation{scope: "all users (test)", targets: windowsTargets(dirs[len(dirs)-1], ""),
		reg: reg, samePath: windowsSamePath, protect: md.protect}
}

func testDirs(base string) []string {
	return []string{filepath.Join(base, "Downex"), filepath.Join(base, "Downex", "nmhost")}
}

func currentUserSID(t *testing.T) *windows.SID {
	t.Helper()
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid, err := tu.User.Sid.Copy()
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

// setDACL gives path the protected DACL of sddl.
func setDACL(t *testing.T, path, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

// TestSystemManifestDirOfAnotherOwner refuses a system-wide registration whose manifest
// directory someone else created first: whoever owns it could rewrite the manifests
// that every user's browser runs.
func TestSystemManifestDirOfAnotherOwner(t *testing.T) {
	dirs := testDirs(t.TempDir())
	// Created by this (unprivileged) test user: not SYSTEM, Administrators or TrustedInstaller.
	if err := os.Mkdir(dirs[0], 0o755); err != nil {
		t.Fatal(err)
	}
	reg := newMemRegistry()
	inst := systemTestInstallation(dirs, reg, manifestDirs{sddl: adminManifestSDDL, trusted: adminSID})
	var out bytes.Buffer
	err := register(inst, testOptions(t, "chrome", "firefox"), &out)
	if err == nil || !strings.Contains(err.Error(), "is owned by") || !strings.Contains(err.Error(), dirs[0]) {
		t.Fatalf("register: %v\n%s", err, out.String())
	}
	if exists(dirs[1]) || len(reg.keys) != 0 || strings.Contains(out.String(), "done:") {
		t.Fatalf("register changed something:\n%s", out.String())
	}
}

// TestSystemManifestDirJunction refuses a manifest directory that is a junction: the
// elevated register would write wherever it points.
func TestSystemManifestDirJunction(t *testing.T) {
	base := t.TempDir()
	dirs := testDirs(base)
	elsewhere := filepath.Join(base, "elsewhere")
	if err := os.Mkdir(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command("cmd", "/c", "mklink", "/J", dirs[0], elsewhere).CombinedOutput(); err != nil {
		t.Skipf("cannot create a junction: %v %s", err, b)
	}
	me := currentUserSID(t)
	trusted := func(s *windows.SID) bool { return adminSID(s) || s.Equals(me) }
	reg := newMemRegistry()
	inst := systemTestInstallation(dirs, reg, manifestDirs{sddl: adminManifestSDDL, trusted: trusted})
	var out bytes.Buffer
	if err := register(inst, testOptions(t, "chrome"), &out); err == nil || !strings.Contains(err.Error(), "junction") {
		t.Fatalf("register: %v\n%s", err, out.String())
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 || len(reg.keys) != 0 {
		t.Fatalf("register wrote through the junction: %v", entries)
	}
}

// TestSystemManifestDirIsSecured creates the manifest directories with a protected DACL,
// gives one that others may change its DACL again, and refuses a manifest that is a
// hard link. The test user stands in for Administrators here.
func TestSystemManifestDirIsSecured(t *testing.T) {
	base := t.TempDir()
	dirs := testDirs(base)
	me := currentUserSID(t)
	trusted := func(s *windows.SID) bool { return adminSID(s) || s.Equals(me) }
	sddl := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + me.String() + ")(A;OICI;0x1200a9;;;BU)"
	// The parent exists already, and Everyone may change it.
	if err := os.Mkdir(dirs[0], 0o755); err != nil {
		t.Fatal(err)
	}
	setDACL(t, dirs[0], "D:P(A;OICI;FA;;;"+me.String()+")(A;OICI;FA;;;WD)")

	reg := newMemRegistry()
	inst := systemTestInstallation(dirs, reg, manifestDirs{sddl: sddl, trusted: trusted})
	o := testOptions(t, "chrome", "firefox")
	var out bytes.Buffer
	if err := register(inst, o, &out); err != nil {
		t.Fatalf("register: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "create or secure") {
		t.Errorf("plan does not secure the directories:\n%s", out.String())
	}
	for _, d := range dirs {
		s, err := inspect(d)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.checkDACL(writeRights, trusted); err != nil {
			t.Errorf("after register: %v", err)
		}
		for i := range uint32(s.dacl.AceCount) {
			var ace *windows.ACCESS_ALLOWED_ACE
			if err := windows.GetAce(s.dacl, i, &ace); err != nil {
				t.Fatal(err)
			}
			if ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
				t.Errorf("%s inherits access control entries from its parent", d)
			}
		}
	}
	if m := readManifest(t, inst.targets["chrome"].file); m.Path != o.exe {
		t.Fatalf("chrome manifest path %q", m.Path)
	}

	// Nothing to change the second time.
	out.Reset()
	if err := register(inst, o, &out); err != nil || !strings.Contains(out.String(), "Nothing to change") {
		t.Fatalf("second register: %v\n%s", err, out.String())
	}

	// A backup that is a hard link to another file is refused, not read.
	secret := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	bak := inst.targets["chrome"].file + backupSuffix
	if err := os.Link(secret, bak); err != nil {
		t.Fatal(err)
	}
	if err := register(inst, o, &out); err == nil || !strings.Contains(err.Error(), "not a plain file") {
		t.Fatalf("register with a linked backup: %v", err)
	}
	if err := os.Remove(bak); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := unregister(inst, o.browsers, false, &out); err != nil {
		t.Fatal(err)
	}
	if exists(inst.targets["chrome"].file) || len(reg.keys) != 0 {
		t.Fatalf("unregister left entries:\n%s", out.String())
	}
}

// TestAdminManifestSDDL checks the DACL given to the system-wide manifest directories:
// protected, changeable only by SYSTEM and Administrators, readable by Users.
func TestAdminManifestSDDL(t *testing.T) {
	sd, err := windows.SecurityDescriptorFromString(adminManifestSDDL)
	if err != nil {
		t.Fatal(err)
	}
	if ctl, _, err := sd.Control(); err != nil || ctl&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("DACL not protected: %v", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := checkACL(dacl, writeRights, adminSID); err != nil {
		t.Fatal(err)
	}
	usersRead := false
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if sid.String() == "S-1-5-32-545" && ace.Mask&windows.FILE_GENERIC_READ == windows.FILE_GENERIC_READ {
			usersRead = true
		}
	}
	if !usersRead {
		t.Fatal("Users cannot read the manifests")
	}
}

// TestSystemHostMustBeAdminOnly refuses to register for all users a host that a user
// could replace, unless --allow-user-writable is given.
func TestSystemHostMustBeAdminOnly(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "downex-nmhost.exe")
	if err := os.WriteFile(exe, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkSystemHost(exe); err == nil {
		t.Fatalf("a host in %s was accepted", dir)
	}
	// System32 is changed only by TrustedInstaller, SYSTEM and Administrators.
	if err := checkSystemHost(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")); err != nil {
		t.Fatalf("cmd.exe: %v", err)
	}
	// downexd next to an admin-only host must be admin-only too (it is checked before
	// the directories, which the test user cannot make admin-only).
	me := currentUserSID(t)
	trusted := func(s *windows.SID) bool { return adminSID(s) || s.Equals(me) }
	daemon := filepath.Join(dir, "downexd.exe")
	if err := os.WriteFile(daemon, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	setDACL(t, daemon, "D:P(A;;FA;;;"+me.String()+")(A;;FA;;;BU)")
	if err := checkHostFiles(exe, trusted); err == nil || !strings.Contains(err.Error(), "downexd.exe") {
		t.Fatalf("a downexd that Users may change: %v", err)
	}

	// The command refuses this test binary, which lives in a user's directory.
	isolateUserDirs(t)
	var stdout, stderr bytes.Buffer
	code := runRegister([]string{"--system", "--browsers", "firefox", "--dry-run"}, &stdout, &stderr)
	if code != exitError || !strings.Contains(stderr.String(), "--allow-user-writable") {
		t.Fatalf("register --system: exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
}

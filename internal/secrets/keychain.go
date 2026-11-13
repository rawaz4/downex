package secrets

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// The macOS keychain backend (Open on darwin). It only runs /usr/bin/security, so it is
// built and tested on every OS with a stand-in tool.

// securityPath is the macOS security(1) tool (overridable in tests).
var securityPath = "/usr/bin/security"

// keychainKeyring stores secrets as generic passwords in the login keychain through
// /usr/bin/security. The service is Config.Service (default "Downex") and the account
// is the secret name. The items are created by security, so their access list trusts
// that tool (Downex reads them back through it, which a build without cgo has no other
// way to do).
type keychainKeyring struct {
	service string
}

// maxSecurityLine bounds a command line of interactive security, which reads lines of
// at most 4096 bytes; longer ones would be split into separate commands.
const maxSecurityLine = 4000

// securityPrompt is the prompt of interactive security (on stdout or stderr).
const securityPrompt = "security> "

// Set stores secret without ever putting it on a command line, where any local process
// could read it: an interactive security (-i) gets the command on its stdin, with the
// password as hex data (-X), which needs no quoting. -U updates an existing item.
// Interactive security reports a failed command on stderr, so any output there other
// than its prompt is a failure.
func (k *keychainKeyring) Set(name string, secret []byte) error {
	if err := checkSet(name, secret); err != nil {
		return err
	}
	if !validName(k.service) {
		return fmt.Errorf("secrets: invalid keychain service %q", k.service)
	}
	// name and service match namePattern: no spaces or quotes that would split the line.
	prefix := "add-generic-password -a " + name + " -s " + k.service + " -U -X "
	if len(prefix)+2*len(secret)+1 > maxSecurityLine {
		return errors.New("secrets: secret too long for the macOS keychain")
	}
	line := make([]byte, 0, len(prefix)+2*len(secret)+1)
	line = append(line, prefix...)
	line = hex.AppendEncode(line, secret)
	line = append(line, '\n')
	defer clear(line)

	cmd := exec.Command(securityPath, "-i")
	cmd.Stdin = bytes.NewReader(line)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr // stdout: the null device (prompts only)
	err := cmd.Run()
	msg := strings.TrimSpace(strings.ReplaceAll(stderr.String(), securityPrompt, ""))
	switch {
	case err != nil && msg == "":
		return fmt.Errorf("secrets: add-generic-password: %w", err)
	case err != nil || msg != "":
		if err == nil {
			err = errors.New("failed")
		}
		// security does not repeat the data it was given; make sure of it.
		msg = strings.ReplaceAll(msg, string(line[len(prefix):len(line)-1]), "<secret>")
		msg = strings.ReplaceAll(msg, string(secret), "<secret>")
		return fmt.Errorf("secrets: add-generic-password: %w: %s", err, msg)
	}
	return nil
}

func (k *keychainKeyring) Get(name string) ([]byte, error) {
	if !validName(name) {
		return nil, fmt.Errorf("secrets: invalid name %q", name)
	}
	cmd := exec.Command(securityPath, "find-generic-password",
		"-a", name, "-s", k.service, "-w")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// security exits 44 when the item is not found.
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("secrets: find-generic-password: %w", err)
	}
	return bytes.TrimRight(out, "\n"), nil
}

func (k *keychainKeyring) Delete(name string) error {
	if !validName(name) {
		return fmt.Errorf("secrets: invalid name %q", name)
	}
	cmd := exec.Command(securityPath, "delete-generic-password",
		"-a", name, "-s", k.service)
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil // not found: already absent
		}
		return fmt.Errorf("secrets: delete-generic-password: %w", err)
	}
	return nil
}

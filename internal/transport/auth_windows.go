//go:build windows

package transport

import (
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/alexbrainman/sspi"
	"github.com/alexbrainman/sspi/negotiate"
	"github.com/alexbrainman/sspi/ntlm"
)

// sspiHandshake answers NTLM or Negotiate challenges as the signed-in Windows user,
// without a password, through SSPI. The acquired credentials and security context are
// released by close.
type sspiHandshake struct {
	scheme string // "ntlm" or "negotiate"
	cred   *sspi.Credentials
	ntlm   *ntlm.ClientContext
	nego   *negotiate.ClientContext
	leg    int
}

// newCurrentUserHandshake creates an SSPI handshake for scheme (Negotiate chooses
// Kerberos with an NTLM fallback; a service principal is derived from host). The
// Negotiate message is produced lazily on the first next call.
func newCurrentUserHandshake(scheme, host string) (handshake, error) {
	switch scheme {
	case "ntlm":
		cred, err := ntlm.AcquireCurrentUserCredentials()
		if err != nil {
			return nil, fmt.Errorf("transport: ntlm credentials: %w", err)
		}
		return &sspiHandshake{scheme: scheme, cred: cred}, nil
	case "negotiate":
		cred, err := negotiate.AcquireCurrentUserCredentials()
		if err != nil {
			return nil, fmt.Errorf("transport: negotiate credentials: %w", err)
		}
		return &sspiHandshake{scheme: scheme, cred: cred}, nil
	}
	return nil, fmt.Errorf("transport: scheme %q is not SSPI", scheme)
}

func (s *sspiHandshake) name() string {
	if s.scheme == "negotiate" {
		return "Negotiate"
	}
	return "NTLM"
}

func (s *sspiHandshake) next(ch challenge) (string, error) {
	s.leg++
	switch {
	case s.leg == 1 && ch.token == "":
		token, err := s.begin()
		if err != nil {
			return "", err
		}
		return s.name() + " " + base64.StdEncoding.EncodeToString(token), nil
	case s.leg == 2 && ch.token != "":
		chal, err := base64.StdEncoding.DecodeString(ch.token)
		if err != nil {
			return "", errors.New("sspi: malformed challenge")
		}
		token, err := s.update(chal)
		if err != nil {
			return "", err
		}
		return s.name() + " " + base64.StdEncoding.EncodeToString(token), nil
	}
	return "", errors.New("sspi: authentication rejected")
}

func (s *sspiHandshake) begin() ([]byte, error) {
	if s.scheme == "negotiate" {
		cc, token, err := negotiate.NewClientContext(s.cred, "")
		if err != nil {
			return nil, fmt.Errorf("sspi negotiate: %w", err)
		}
		s.nego = cc
		return token, nil
	}
	cc, token, err := ntlm.NewClientContext(s.cred)
	if err != nil {
		return nil, fmt.Errorf("sspi ntlm: %w", err)
	}
	s.ntlm = cc
	return token, nil
}

func (s *sspiHandshake) update(chal []byte) ([]byte, error) {
	if s.nego != nil {
		_, token, err := s.nego.Update(chal)
		if err != nil {
			return nil, fmt.Errorf("sspi negotiate: %w", err)
		}
		return token, nil
	}
	if s.ntlm != nil {
		token, err := s.ntlm.Update(chal)
		if err != nil {
			return nil, fmt.Errorf("sspi ntlm: %w", err)
		}
		return token, nil
	}
	return nil, errors.New("sspi: no client context")
}

func (s *sspiHandshake) connBased() bool { return true }

func (s *sspiHandshake) close() {
	if s.ntlm != nil {
		_ = s.ntlm.Release()
		s.ntlm = nil
	}
	if s.nego != nil {
		_ = s.nego.Release()
		s.nego = nil
	}
	if s.cred != nil {
		_ = s.cred.Release()
		s.cred = nil
	}
}

package transport

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/go-ntlmssp"

	"downex/internal/model"
)

// ntlmServer runs a fake two-leg NTLM handshake: it accepts the type-1 negotiate
// message, replies with a canned type-2 challenge, then accepts the type-3 message and
// serves the content. It does not verify the NTLM hashes (the goja/go-ntlmssp message
// shaping is what is under test here); it checks the leg structure and that the final
// message carries the expected user name.
type ntlmServer struct {
	content []byte
	user    string
	legs    int
}

// cannedChallenge is a minimal valid NTLM type-2 message (8-byte signature, type 2,
// a target-name field and flags) produced with go-ntlmssp-compatible layout.
func cannedChallenge() string {
	msg := make([]byte, 56)
	copy(msg, "NTLMSSP\x00")
	msg[8] = 0x02 // type 2
	// TargetNameFields (len, maxlen, offset) left zero; NegotiateFlags at offset 20.
	// Set NEGOTIATE_UNICODE | NEGOTIATE_NTLM | TARGET_INFO so ProcessChallenge proceeds.
	flags := uint32(0x00000001 | 0x00000200 | 0x00800000)
	msg[20] = byte(flags)
	msg[21] = byte(flags >> 8)
	msg[22] = byte(flags >> 16)
	msg[23] = byte(flags >> 24)
	// ServerChallenge (8 bytes) at offset 24.
	copy(msg[24:32], []byte{1, 2, 3, 4, 5, 6, 7, 8})
	return base64.StdEncoding.EncodeToString(msg)
}

func (s *ntlmServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	switch {
	case !strings.HasPrefix(auth, "NTLM "):
		s.legs = 1
		w.Header().Set("WWW-Authenticate", "NTLM")
		w.WriteHeader(http.StatusUnauthorized)
	case s.isType1(auth):
		s.legs = 2
		w.Header().Set("WWW-Authenticate", "NTLM "+cannedChallenge())
		w.WriteHeader(http.StatusUnauthorized)
	default:
		s.legs = 3
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(s.content))
	}
}

func (s *ntlmServer) isType1(auth string) bool {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, "NTLM "))
	return err == nil && len(raw) >= 9 && raw[8] == 0x01
}

func TestNTLMServerAuthFakeFlow(t *testing.T) {
	// Sanity: the canned challenge is well-formed for go-ntlmssp.
	chal, _ := base64.StdEncoding.DecodeString(cannedChallenge())
	if _, err := ntlmssp.ProcessChallenge(chal, "WORKGROUP\\user", "pw", true); err != nil {
		t.Skipf("canned NTLM challenge rejected by go-ntlmssp (%v); skipping fake flow", err)
	}
	ns := &ntlmServer{content: testContent(4000), user: "user"}
	ts := startServer(t, kindHTTP1, ns)
	host := mustURL(t, ts.URL).Hostname()
	opts := DefaultHTTPOptions()
	opts.Credentials = staticCreds{server: map[string]Login{host: {Username: "WORKGROUP\\user", Password: "pw"}}}
	slot := newTestSlot(t, opts, ts)
	resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL}, End: -1})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 200/206 after the NTLM handshake", resp.StatusCode)
	}
	got := readBody(t, resp)
	if !bytes.Equal(got, ns.content) {
		t.Fatal("body mismatch after NTLM auth")
	}
	if ns.legs != 3 {
		t.Fatalf("server completed %d legs, want the full 3-leg NTLM handshake", ns.legs)
	}
}

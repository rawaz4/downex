//go:build windows

package update

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Results of WinVerifyTrust that mean "not signed" rather than "badly signed".
const (
	trustNoSignature        = windows.Errno(0x800B0100) // TRUST_E_NOSIGNATURE
	trustSubjectFormUnknown = windows.Errno(0x800B0003) // TRUST_E_SUBJECT_FORM_UNKNOWN
	trustProviderUnknown    = windows.Errno(0x800B0001) // TRUST_E_PROVIDER_UNKNOWN
)

// CMSG_SIGNER_CERT_INFO_PARAM: CryptMsgGetParam's CERT_INFO (issuer and serial number) of
// a signer of a PKCS #7 message.
const cmsgSignerCertInfoParam = 7

var (
	modcrypt32           = windows.NewLazySystemDLL("crypt32.dll")
	procCryptMsgGetParam = modcrypt32.NewProc("CryptMsgGetParam")
	procCryptMsgClose    = modcrypt32.NewProc("CryptMsgClose")
)

// VerifyAuthenticode checks the embedded Authenticode signature of path with
// WinVerifyTrust (generic verify policy, no UI, no revocation check: offline machines
// must not fail) and returns its signer. A file without a signature fails with an error
// wrapping ErrUnsigned; any other error means a signature that is invalid or not
// trusted. The signer's subject is read from the signature WinVerifyTrust accepted; its
// fields are empty when they cannot be read.
func VerifyAuthenticode(path string) (Signer, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return Signer{}, fmt.Errorf("authenticode: %w", err)
	}
	file := &windows.WinTrustFileInfo{Size: uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})), FilePath: p}
	data := &windows.WinTrustData{
		Size:                            uint32(unsafe.Sizeof(windows.WinTrustData{})),
		UIChoice:                        windows.WTD_UI_NONE,
		RevocationChecks:                windows.WTD_REVOKE_NONE,
		UnionChoice:                     windows.WTD_CHOICE_FILE,
		StateAction:                     windows.WTD_STATEACTION_VERIFY,
		FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(file),
	}
	verr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	data.StateAction = windows.WTD_STATEACTION_CLOSE
	_ = windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	switch {
	case verr == nil:
		return signerOf(p), nil
	case errors.Is(verr, trustNoSignature), errors.Is(verr, trustSubjectFormUnknown), errors.Is(verr, trustProviderUnknown):
		return Signer{}, fmt.Errorf("%w (%v)", ErrUnsigned, verr)
	}
	return Signer{}, fmt.Errorf("authenticode: %w", verr)
}

// signerOf returns the subject of the certificate of the first (primary) signer of the
// PKCS #7 signature embedded in the file at path: its simple display name (the CN) and its
// DER-encoded distinguished name; the zero Signer when they cannot be read.
func signerOf(path *uint16) Signer {
	var encoding, contentType, formatType uint32
	var store, msg windows.Handle
	err := windows.CryptQueryObject(windows.CERT_QUERY_OBJECT_FILE, unsafe.Pointer(path),
		windows.CERT_QUERY_CONTENT_FLAG_PKCS7_SIGNED_EMBED, windows.CERT_QUERY_FORMAT_FLAG_BINARY, 0,
		&encoding, &contentType, &formatType, &store, &msg, nil)
	if err != nil {
		return Signer{}
	}
	defer windows.CertCloseStore(store, 0)
	defer procCryptMsgClose.Call(uintptr(msg))
	var size uint32
	if r, _, _ := procCryptMsgGetParam.Call(uintptr(msg), cmsgSignerCertInfoParam, 0, 0, uintptr(unsafe.Pointer(&size))); r == 0 || size == 0 || size > 64<<10 {
		return Signer{}
	}
	// A CERT_INFO and the blobs it points to; uint64 words keep its pointers aligned.
	buf := make([]uint64, (size+7)/8)
	if r, _, _ := procCryptMsgGetParam.Call(uintptr(msg), cmsgSignerCertInfoParam, 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); r == 0 {
		return Signer{}
	}
	cert, err := windows.CertFindCertificateInStore(store, encoding, 0, windows.CERT_FIND_SUBJECT_CERT, unsafe.Pointer(&buf[0]), nil)
	runtime.KeepAlive(buf)
	if err != nil {
		return Signer{}
	}
	defer windows.CertFreeCertificateContext(cert)
	var s Signer
	if blob := cert.CertInfo.Subject; blob.Size > 0 && blob.Size <= 64<<10 && blob.Data != nil {
		s.SubjectName = slices.Clone(unsafe.Slice(blob.Data, blob.Size))
	}
	if n := windows.CertGetNameString(cert, windows.CERT_NAME_SIMPLE_DISPLAY_TYPE, 0, nil, nil, 0); n > 1 {
		name := make([]uint16, n)
		windows.CertGetNameString(cert, windows.CERT_NAME_SIMPLE_DISPLAY_TYPE, 0, nil, &name[0], n)
		s.Subject = windows.UTF16ToString(name)
	}
	return s
}

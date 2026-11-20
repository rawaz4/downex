package sitejob

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// createPrivate creates a new file at path that only the user can open: owned by the
// user, with a protected DACL that grants the user full access and nobody else
// anything (docs/SITE-MEDIA-SPEC.md §6.3), set when the file is created; an existing
// file is an error.
func createPrivate(path string) (*os.File, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("query the current user: %w", err)
	}
	s := tu.User.Sid.String()
	sd, err := windows.SecurityDescriptorFromString("O:" + s + "D:P(A;;FA;;;" + s + ")")
	if err != nil {
		return nil, fmt.Errorf("build the security descriptor: %w", err)
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	p16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p16, windows.GENERIC_WRITE, 0, sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "create", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

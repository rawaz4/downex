//go:build windows

package win

import (
	"crypto/sha1"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"downex/ui/win/internal/com"
)

// The shell's common item dialog (ShObjIdl_core.h): IFileOpenDialog, which picks a file
// or, with FOS_PICKFOLDERS, a folder, and IShellItem, which names the start folder and the
// choice. The slots and GUIDs are checked against the SDK headers (picker_test.go).

var (
	clsidFileOpenDialog = com.MustGUID("dc1c5a9c-e88a-4dde-a5a1-60f82a20aef7")
	iidFileOpenDialog   = com.MustGUID("d57c7288-d4ad-4768-be02-9d969532d960")
	iidShellItem        = com.MustGUID("43826d1e-e718-42ee-bc55-a1e261c37bfe")
)

// IFileOpenDialog's slots: IUnknown 0-2, IModalWindow 3, IFileDialog 4-26.
const (
	slotFDShow          = 3
	slotFDSetFileTypes  = 4
	slotFDSetOptions    = 9
	slotFDGetOptions    = 10
	slotFDSetFolder     = 12
	slotFDSetTitle      = 17
	slotFDGetResult     = 20
	slotFDSetClientGuid = 24
)

// IShellItem's GetDisplayName.
const slotSIGetDisplayName = 5

const (
	fosPickFolders   = 0x00000020
	sigdnFileSysPath = 0x80058000
	hrErrorCancelled = 0x800704C7 // HRESULT_FROM_WIN32(ERROR_CANCELLED): the user closed the dialog
)

type fileDialog struct{ com.Object }

type shellItem struct{ com.Object }

// comFilterSpec is COMDLG_FILTERSPEC.
type comFilterSpec struct {
	name, spec *uint16
}

func (d *fileDialog) this() uintptr { return uintptr(unsafe.Pointer(d)) }

// show runs the dialog's modal loop: nil when the user chose, errCancelled when they closed
// it.
func (d *fileDialog) show(owner uintptr) error {
	r, _, _ := syscall.SyscallN(d.Method(slotFDShow), d.this(), owner)
	if uint32(r) == hrErrorCancelled {
		return errCancelled
	}
	return com.Check("IFileDialog.Show", com.HRESULT(r))
}

var errCancelled = fmt.Errorf("cancelled")

func (d *fileDialog) addOptions(fos uint32) error {
	var opts uint32
	r, _, _ := syscall.SyscallN(d.Method(slotFDGetOptions), d.this(), uintptr(unsafe.Pointer(&opts)))
	if err := com.Check("IFileDialog.GetOptions", com.HRESULT(r)); err != nil {
		return err
	}
	r, _, _ = syscall.SyscallN(d.Method(slotFDSetOptions), d.this(), uintptr(opts|fos))
	return com.Check("IFileDialog.SetOptions", com.HRESULT(r))
}

func (d *fileDialog) setTitle(title string) error {
	p, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return fmt.Errorf("picker title: %w", err)
	}
	r, _, _ := syscall.SyscallN(d.Method(slotFDSetTitle), d.this(), uintptr(unsafe.Pointer(p)))
	return com.Check("IFileDialog.SetTitle", com.HRESULT(r))
}

func (d *fileDialog) setClientGUID(g *com.GUID) error {
	r, _, _ := syscall.SyscallN(d.Method(slotFDSetClientGuid), d.this(), uintptr(unsafe.Pointer(g)))
	return com.Check("IFileDialog.SetClientGuid", com.HRESULT(r))
}

func (d *fileDialog) setFileTypes(specs []comFilterSpec) error {
	if len(specs) == 0 {
		return nil
	}
	r, _, _ := syscall.SyscallN(d.Method(slotFDSetFileTypes), d.this(), uintptr(len(specs)), uintptr(unsafe.Pointer(&specs[0])))
	return com.Check("IFileDialog.SetFileTypes", com.HRESULT(r))
}

func (d *fileDialog) setFolder(path string) error {
	item, err := shellItemOf(path)
	if err != nil {
		return err
	}
	defer item.Release()
	r, _, _ := syscall.SyscallN(d.Method(slotFDSetFolder), d.this(), uintptr(unsafe.Pointer(item)))
	return com.Check("IFileDialog.SetFolder", com.HRESULT(r))
}

// result is the chosen item's file-system path.
func (d *fileDialog) result() (string, error) {
	var item *shellItem
	r, _, _ := syscall.SyscallN(d.Method(slotFDGetResult), d.this(), uintptr(unsafe.Pointer(&item)))
	if err := com.Check("IFileDialog.GetResult", com.HRESULT(r)); err != nil {
		return "", err
	}
	if item == nil {
		return "", errCancelled
	}
	defer item.Release()
	return item.fileSysPath()
}

func shellItemOf(path string) (*shellItem, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("picker folder: %w", err)
	}
	var item *shellItem
	r, _, _ := syscall.SyscallN(procSHCreateItemFromParsingNm.Addr(), uintptr(unsafe.Pointer(p)), 0,
		uintptr(unsafe.Pointer(&iidShellItem)), uintptr(unsafe.Pointer(&item)))
	if err := com.Check("SHCreateItemFromParsingName", com.HRESULT(r)); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *shellItem) fileSysPath() (string, error) {
	var name *uint16
	r, _, _ := syscall.SyscallN(s.Method(slotSIGetDisplayName), uintptr(unsafe.Pointer(s)), sigdnFileSysPath, uintptr(unsafe.Pointer(&name)))
	if err := com.Check("IShellItem.GetDisplayName", com.HRESULT(r)); err != nil {
		return "", err
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(name))
	return windows.UTF16PtrToString(name), nil
}

// roleGUID is the dialog's client GUID for a role name, as today's window's dialog library
// makes it: the name-based (SHA-1, version 5) UUID of the name in the nil namespace. The
// dialog keeps its last folder and size per client GUID, so the native folder picker
// remembers what today's remembered.
func roleGUID(role string) com.GUID {
	h := sha1.New()
	h.Write(make([]byte, 16)) // the nil namespace
	h.Write([]byte(role))
	b := h.Sum(nil)[:16]
	b[6] = b[6]&0x0f | 0x50
	b[8] = b[8]&0x3f | 0x80
	return com.GUID{
		Data1: uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]),
		Data2: uint16(b[4])<<8 | uint16(b[5]),
		Data3: uint16(b[6])<<8 | uint16(b[7]),
		Data4: [8]byte(b[8:16]),
	}
}

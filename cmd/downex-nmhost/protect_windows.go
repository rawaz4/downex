//go:build windows

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"downex/internal/client"
)

// A system-wide registration makes every user's browser run the files it names, so
// only administrators may be able to change them (docs/BROWSER-SPEC.md §1): the host
// executable, downexd next to it, the manifests, and every directory above them.

// adminManifestSDDL is the protected DACL of the system-wide manifest directories:
// full control for SYSTEM and Administrators, read and execute for Users, inherited
// by everything below.
const adminManifestSDDL = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1200a9;;;BU)"

// Principals trusted to change system-wide files: SYSTEM, Administrators and
// TrustedInstaller; OWNER RIGHTS, whose owner is checked to be one of those; and
// CREATOR OWNER and CREATOR GROUP, which grant nothing on the object itself.
var adminSIDs = map[string]bool{
	"S-1-5-18":     true,
	"S-1-5-32-544": true,
	"S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464": true,
	"S-1-3-4": true,
	"S-1-3-0": true,
	"S-1-3-1": true,
}

func adminSID(s *windows.SID) bool { return adminSIDs[s.String()] }

// Access rights that matter for system-wide files.
const (
	fileDeleteChild = 0x40 // FILE_DELETE_CHILD
	// replaceRights let a principal rename, delete or re-permission an object, or the
	// entries of a directory: what matters on the directories above a file.
	replaceRights = windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | fileDeleteChild |
		windows.GENERIC_ALL | windows.MAXIMUM_ALLOWED
	// writeRights also let it change a file or add entries to a directory.
	writeRights = replaceRights | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.GENERIC_WRITE
)

// ACE types (winnt.h) beyond those x/sys names.
const (
	accessAllowedACEType         = 0x0
	accessDeniedACEType          = 0x1
	accessAllowedCallbackACEType = 0x9
	accessDeniedCallbackACEType  = 0xa
)

// fsSecurity is what checks read of one file or directory, from a handle opened on the
// object itself (a link or junction is not followed).
type fsSecurity struct {
	path  string
	attrs uint32
	links uint32
	owner *windows.SID
	dacl  *windows.ACL // nil: a NULL DACL, which grants everyone everything
}

// inspect reads the attributes, owner and DACL of path. The error matches
// fs.ErrNotExist when there is nothing at path.
func inspect(path string) (*fsSecurity, error) {
	p16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p16, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w", path, fs.ErrNotExist)
		}
		return nil, fmt.Errorf("cannot check who may change %s: %w", path, err)
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return nil, fmt.Errorf("cannot check %s: %w", path, err)
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, fmt.Errorf("cannot check who may change %s: %w", path, err)
	}
	s := &fsSecurity{path: path, attrs: info.FileAttributes, links: info.NumberOfLinks}
	if s.owner, _, err = sd.Owner(); err != nil || s.owner == nil {
		return nil, fmt.Errorf("cannot read the owner of %s: %v", path, err)
	}
	// owner and dacl point into sd, a Go copy that they keep alive.
	if s.dacl, _, err = sd.DACL(); err != nil {
		return nil, fmt.Errorf("cannot read the access control list of %s: %w", path, err)
	}
	return s, nil
}

func (s *fsSecurity) isDir() bool { return s.attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0 }

// checkOwner refuses a link or junction, and an object that is not owned by trusted.
func (s *fsSecurity) checkOwner(trusted func(*windows.SID) bool) error {
	if s.attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("%s is a link or junction (reparse point)", s.path)
	}
	if !trusted(s.owner) {
		return fmt.Errorf("%s is owned by %s, not by SYSTEM, Administrators or TrustedInstaller", s.path, account(s.owner))
	}
	return nil
}

// checkDACL refuses an object whose DACL grants any of mask to a principal that is
// not trusted.
func (s *fsSecurity) checkDACL(mask uint32, trusted func(*windows.SID) bool) error {
	if err := checkACL(s.dacl, mask, trusted); err != nil {
		return fmt.Errorf("%s %w", s.path, err)
	}
	return nil
}

// checkACL refuses a DACL that grants any of mask to a principal that is not trusted.
// Entries that only pass on to children are ignored; deny entries cannot add rights.
func checkACL(dacl *windows.ACL, mask uint32, trusted func(*windows.SID) bool) error {
	if dacl == nil {
		return errors.New("has no access control list: everyone may change it")
	}
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("has an unreadable access control entry: %w", err)
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		switch ace.Header.AceType {
		case accessDeniedACEType, accessDeniedCallbackACEType:
			continue
		case accessAllowedACEType, accessAllowedCallbackACEType:
		default:
			return fmt.Errorf("has an access control entry of unsupported type %d", ace.Header.AceType)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if uint32(ace.Mask)&mask != 0 && !trusted(sid) {
			return fmt.Errorf("can be changed by %s", account(sid))
		}
	}
	return nil
}

// account names sid for messages.
func account(sid *windows.SID) string {
	if name, domain, _, err := sid.LookupAccount(""); err == nil {
		if domain != "" {
			return domain + `\` + name
		}
		return name
	}
	return sid.String()
}

// checkAdminOnly refuses path unless only trusted principals can change it with mask.
func checkAdminOnly(path string, mask uint32, trusted func(*windows.SID) bool) error {
	s, err := inspect(path)
	if err != nil {
		return err
	}
	if err := s.checkOwner(trusted); err != nil {
		return err
	}
	return s.checkDACL(mask, trusted)
}

// checkParents refuses dir's parents unless only trusted principals can rename,
// delete or re-permission them.
func checkParents(dir string, trusted func(*windows.SID) bool) error {
	for p := dir; ; {
		parent := filepath.Dir(p)
		if parent == p {
			return nil
		}
		if err := checkAdminOnly(parent, replaceRights, trusted); err != nil {
			return err
		}
		p = parent
	}
}

// checkSystemHost refuses a host for a system-wide registration that anyone but
// administrators could replace: the executable, downexd next to it (which the relay
// starts), their directory, or a directory above it.
func checkSystemHost(exe string) error {
	return checkHostFiles(exe, adminSID)
}

func checkHostFiles(exe string, trusted func(*windows.SID) bool) error {
	dir := filepath.Dir(exe)
	if err := checkAdminOnly(exe, writeRights, trusted); err != nil {
		return err
	}
	daemon := filepath.Join(dir, client.DaemonName())
	if err := checkAdminOnly(daemon, writeRights, trusted); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := checkAdminOnly(dir, writeRights, trusted); err != nil {
		return err
	}
	return checkParents(dir, trusted)
}

// manifestDirs are the directories of the system-wide manifests: created, or given
// their DACL again, so that only administrators can change what is in them.
type manifestDirs struct {
	// base is the existing directory dirs are below (%ProgramData%); it and its parents
	// must not be replaceable by others. "" skips that check (tests).
	base string
	// dirs are created or secured, parents first.
	dirs []string
	// sddl is the DACL given to dirs.
	sddl string
	// trusted are the principals that may change them.
	trusted func(*windows.SID) bool
}

// protect checks the directories and the existing manifest files before register plans
// anything, and returns the step that creates the directories or gives them their DACL
// again, or nil when they already exist and only trusted principals can change them.
// A directory, manifest or backup that someone else owns, or that is a link or
// junction, is refused: it must be removed (after checking it) first.
func (m manifestDirs) protect(files []string) (*step, error) {
	if m.base != "" {
		if err := checkAdminOnly(m.base, replaceRights, m.trusted); err != nil {
			return nil, err
		}
		if err := checkParents(m.base, m.trusted); err != nil {
			return nil, err
		}
	}
	needed := false
	for _, d := range m.dirs {
		s, err := inspect(d)
		if errors.Is(err, fs.ErrNotExist) {
			needed = true
			continue
		}
		if err != nil {
			return nil, removeHint(err)
		}
		if err := m.checkDir(s); err != nil {
			return nil, err
		}
		if s.checkDACL(writeRights, m.trusted) != nil {
			needed = true
		}
	}
	for _, f := range files {
		s, err := inspect(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, removeHint(err)
		}
		if err := s.checkOwner(m.trusted); err != nil {
			return nil, removeHint(err)
		}
		if s.isDir() || s.links > 1 {
			return nil, removeHint(fmt.Errorf("%s is not a plain file", f))
		}
		if err := s.checkDACL(writeRights, m.trusted); err != nil {
			return nil, removeHint(err)
		}
	}
	if !needed {
		return nil, nil
	}
	return &step{
		desc: fmt.Sprintf("create or secure %s (only administrators may change it)", strings.Join(m.dirs, ", ")),
		do:   m.secure,
	}, nil
}

// checkDir refuses an existing directory that may not be secured: not a directory, a
// link or junction, or owned by someone untrusted.
func (m manifestDirs) checkDir(s *fsSecurity) error {
	if err := s.checkOwner(m.trusted); err != nil {
		return removeHint(err)
	}
	if !s.isDir() {
		return removeHint(fmt.Errorf("%s is not a directory", s.path))
	}
	return nil
}

// secure creates each directory with the DACL, or gives an existing one the DACL again
// after checking it once more.
func (m manifestDirs) secure() error {
	sd, err := windows.SecurityDescriptorFromString(m.sddl)
	if err != nil {
		return fmt.Errorf("build the manifest directory security descriptor: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("build the manifest directory security descriptor: %w", err)
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	for _, d := range m.dirs {
		p16, err := windows.UTF16PtrFromString(d)
		if err != nil {
			return err
		}
		err = windows.CreateDirectory(p16, sa)
		if err == nil {
			continue
		}
		if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return fmt.Errorf("create %s: %w", d, err)
		}
		h, err := windows.CreateFile(p16, windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_ATTRIBUTES,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
			windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
		if err != nil {
			return fmt.Errorf("open %s: %w", d, err)
		}
		err = m.secureHandle(h, d, dacl)
		windows.CloseHandle(h)
		if err != nil {
			return err
		}
	}
	return nil
}

func (m manifestDirs) secureHandle(h windows.Handle, path string, dacl *windows.ACL) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return fmt.Errorf("check %s: %w", path, err)
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("check %s: %w", path, err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return fmt.Errorf("check the owner of %s: %v", path, err)
	}
	if err := m.checkDir(&fsSecurity{path: path, attrs: info.FileAttributes, owner: owner}); err != nil {
		return err
	}
	if err := windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("set the access control list of %s: %w", path, err)
	}
	return nil
}

func removeHint(err error) error {
	return fmt.Errorf("%w; a system-wide registration needs locations only administrators can change: "+
		"remove it (after checking what it is) and register again", err)
}

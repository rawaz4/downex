package sitejob

import (
	"testing"

	"golang.org/x/sys/windows"
)

// checkPrivate checks that path has a protected DACL with one entry, which grants the
// current user full access, and is owned by that user (docs/SITE-MEDIA-SPEC.md §6.3).
func checkPrivate(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("security of %s: %v", path, err)
	}
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	me := tu.User.Sid.String()
	if want := "O:" + me + "D:P(A;;FA;;;" + me + ")"; sd.String() != want {
		t.Errorf("%s: security %s, want %s", path, sd.String(), want)
	}
}

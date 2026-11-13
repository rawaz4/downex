//go:build windows

package daemon

import (
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// The Downloads folder is trusted wherever it is (folder redirection to a network
// share), and so are the category folders below it: Confirm accepts them although
// they are not plain local paths (GO-2). A \\?\ path stands in for \\server\share
// here, so that the test needs no network: checkLocalDir refuses both alike.
func TestConfirmIntoCategoryFolderOfRedirectedDownloads(t *testing.T) {
	const size, seed = 32 << 10, 70
	ts := serve(t, testserver.Options{Size: size, Seed: seed})
	downloads := `\\?\` + t.TempDir()
	h := newHarness(t, harnessOptions{dir: downloads, openConfirmation: (&launcher{}).open})
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/v/movie.mp4", Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	if d.GetState() != stAwaiting || d.GetDir() != filepath.Join(downloads, "Video") {
		t.Fatalf("added = %v", d)
	}
	archives := filepath.Join(downloads, "Archives")
	res, err := h.dl.Confirm(ctxT(t), connect.NewRequest(&downexv1.ConfirmRequest{Id: d.GetId(), Dir: archives, Start: true}))
	if err != nil {
		t.Fatalf("confirm into the Archives folder of the redirected Downloads folder: %v", err)
	}
	if c := res.Msg.GetDownload(); c.GetDir() != archives || c.GetCategory() != "Archives" {
		t.Fatalf("confirmed = %v", c)
	}
	checkFile(t, h.waitCompleted(d.GetId()), seed, size)

	// Folders a client picks elsewhere off the local drives are still refused.
	o := h.add(&downexv1.AddRequest{Url: ts.URL + "/o/other.zip", Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	_, err = h.dl.Confirm(ctxT(t), connect.NewRequest(&downexv1.ConfirmRequest{Id: o.GetId(), Dir: filepath.Join(downloads, "Picked")}))
	if connectCode(err) != connect.CodeInvalidArgument {
		t.Fatalf("confirm into another non-local folder: %v", err)
	}
}

package scene_test

import (
	"context"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/state/scene"
)

// TestCheckFixture: the "check" token gives the scene's downloads the file checks downexd
// reports, with its texts (docs/DESKTOP-SPEC.md §9), and adds the disguised hand-off.
func TestCheckFixture(t *testing.T) {
	m, err := scene.New(scene.Options{Fixture: "check"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	res, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*downexv1.Download{}
	for _, d := range res.GetDownloads() {
		byName[d.GetFilename()] = d
	}
	want := map[string]downexv1.FileCheckLevel{
		"drone-footage-coast.mp4":  downexv1.FileCheckLevel_FILE_CHECK_LEVEL_DANGER,
		"podcast-episode-41.mp3":   downexv1.FileCheckLevel_FILE_CHECK_LEVEL_CAUTION,
		"ambient-loop-3.mp3":       downexv1.FileCheckLevel_FILE_CHECK_LEVEL_NOTE,
		"os-image-26.04-amd64.iso": downexv1.FileCheckLevel_FILE_CHECK_LEVEL_DANGER,
		"invoice.pdf.exe":          downexv1.FileCheckLevel_FILE_CHECK_LEVEL_DANGER,
	}
	if len(want) != len(scene.FixtureCheckNames) {
		t.Fatal("FixtureCheckNames")
	}
	for _, name := range scene.FixtureCheckNames {
		d := byName[name]
		if d == nil {
			t.Fatalf("no %s", name)
		}
		if fc := d.GetFileCheck(); fc.GetLevel() != want[name] || fc.GetSummary() == "" {
			t.Errorf("%s: %v", name, fc)
		}
	}
	if got := byName["drone-footage-coast.mp4"].GetFileCheck().GetSummary(); got != "This file is a Windows program, not an MP4 video." {
		t.Errorf("summary %q", got)
	}
	plain, err := scene.New(scene.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(plain.Close)
	res, err = plain.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range res.GetDownloads() {
		if d.GetFileCheck() != nil {
			t.Errorf("the Studio scene without the token has a check on %s", d.GetFilename())
		}
	}
}

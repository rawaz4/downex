package prefs_test

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"downex/ui/state"
	"downex/ui/state/prefs"
)

// FuzzOpen: prefs.Open never crashes on any input, neither on what the file holds nor, for
// a file that does not exist yet, on the WebView2 folder it imports from (a .ldb table and
// a .log). Run with go test -run='^$' -fuzz='^FuzzOpen$' -fuzztime=60s.
func FuzzOpen(f *testing.F) {
	footer := make([]byte, 64)
	foot := binary.AppendUvarint(binary.AppendUvarint(nil, 0), 0)
	foot = binary.AppendUvarint(binary.AppendUvarint(foot, ^uint64(0)-9), 7) // the re-check's footer
	foot = append(foot, make([]byte, 40-len(foot))...)
	footer = binary.LittleEndian.AppendUint64(append(footer, foot...), 0xdb4775248b80fb57)
	f.Add(true, []byte(`{"downex.theme":"dark","downex.later":{"a":1}}`), []byte{}, []byte{})
	f.Add(true, []byte("not json"), []byte{}, []byte{})
	f.Add(true, []byte(`null`), []byte{}, []byte{})
	f.Add(false, []byte{}, footer, []byte{})
	f.Add(false, []byte{}, []byte("not a table"), []byte{0, 0, 0, 0, 5, 0, 1, 'a', 'b', 'c', 'd', 'e'})
	f.Fuzz(func(t *testing.T, exists bool, file, ldb, log []byte) {
		dir := t.TempDir()
		path := filepath.Join(dir, prefs.FileName)
		if exists {
			if err := os.WriteFile(path, file, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		storage := filepath.Join(dir, "leveldb")
		if err := os.Mkdir(storage, 0o700); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(storage, "000003.ldb"), ldb, 0o600)
		_ = os.WriteFile(filepath.Join(storage, "000004.log"), log, 0o600)
		p, err := prefs.Open(path, prefs.Options{Poll: -1, Import: prefs.WebView2Import(storage)})
		if err != nil {
			t.Fatal(err)
		}
		_ = p.Values()
		_ = state.ColumnWidths(p)
		if err := p.Set(state.ThemeKey, "dark"); err != nil {
			t.Fatal(err)
		}
		if err := p.Flush(); err != nil {
			t.Fatal(err)
		}
		p.Close()
	})
}

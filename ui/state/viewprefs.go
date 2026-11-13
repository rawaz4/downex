package state

import (
	"encoding/json"
	"io"
	"math"
	"strconv"
	"strings"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/display"
)

// The per-viewer conveniences other than theme and density, read and written as today's
// window does. They are conveniences: a storage that fails gives the default, and a write
// that fails is dropped (the choice lasts for the session).

// ColumnWidths are the saved column widths in CSS px by column key (DownloadTable.svelte
// loadWidths): only resizable columns (a width), with a finite positive number.
func ColumnWidths(s Storage) map[string]float64 {
	out := map[string]float64{}
	if s == nil {
		return out
	}
	raw, ok, err := s.Get(ColumnWidthsKey)
	if err != nil || !ok {
		return out
	}
	// Numbers are read as JSON.parse reads them: one too large for a double is Infinity
	// (and dropped below), not a failure of the whole object.
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil {
		return out
	}
	if _, err := dec.Token(); err != io.EOF {
		return out // text after the object: JSON.parse throws
	}
	for _, c := range display.Columns() {
		n, isNum := m[c.Key].(json.Number)
		if !isNum || c.Width <= 0 {
			continue
		}
		v, err := strconv.ParseFloat(string(n), 64)
		if err == nil && !math.IsInf(v, 0) && !math.IsNaN(v) && v > 0 {
			out[c.Key] = v
		}
	}
	return out
}

// SaveColumnWidths stores widths as JSON (saveWidths).
func SaveColumnWidths(s Storage, widths map[string]float64) {
	if s == nil {
		return
	}
	data, err := json.Marshal(widths)
	if err == nil {
		_ = s.Set(ColumnWidthsKey, string(data))
	}
}

// ExistingFileChoice is the Download again dialog's remembered choice (RestartDialog.svelte):
// Replace when "replace" is stored, else KeepBoth.
func ExistingFileChoice(s Storage) downexv1.ExistingFile {
	if s != nil {
		if v, _, err := s.Get(ExistingFileKey); err == nil && v == "replace" {
			return Replace
		}
	}
	return KeepBoth
}

// SaveExistingFileChoice remembers the choice ("replace" or "keepBoth").
func SaveExistingFileChoice(s Storage, c downexv1.ExistingFile) {
	if s == nil {
		return
	}
	v := "keepBoth"
	if c == Replace {
		v = "replace"
	}
	_ = s.Set(ExistingFileKey, v)
}

func flag(s Storage, key string) bool {
	if s == nil {
		return false
	}
	v, _, err := s.Get(key)
	return err == nil && v == "1"
}

func setFlag(s Storage, key string, on bool) {
	if s == nil {
		return
	}
	v := "0"
	if on {
		v = "1"
	}
	_ = s.Set(key, v)
}

// ProgressDetails reports that a progress window opens expanded (ProgressWindow.svelte).
func ProgressDetails(s Storage) bool { return flag(s, ProgressDetailsKey) }

// SaveProgressDetails remembers it ("1" or "0").
func SaveProgressDetails(s Storage, expanded bool) { setFlag(s, ProgressDetailsKey, expanded) }

// DetailsCollapsed reports that the main window's details panel is collapsed (App.svelte).
func DetailsCollapsed(s Storage) bool { return flag(s, DetailsCollapsedKey) }

// SaveDetailsCollapsed remembers it ("1" or "0").
func SaveDetailsCollapsed(s Storage, collapsed bool) { setFlag(s, DetailsCollapsedKey, collapsed) }

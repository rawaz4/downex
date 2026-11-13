package widgets_test

import (
	"slices"
	"testing"

	"downex/ui/core/queues"
	"downex/ui/kit"
	"downex/ui/kit/hostcap/hostcaptest"
	"downex/ui/kit/widgets"
)

// The DateTimeField.test.ts cases of the keyboard; its rules' cases (parseParts, formatParts,
// partAt, stepParts) are ui/core/queues' TestDateTimeFieldRules, which owns them.

type dtRig struct {
	*rig
	f     *widgets.DateTimeField
	field *hostcaptest.Field
}

func setupDateTime(t *testing.T, kind queues.FieldKind, value, fallback string) *dtRig {
	t.Helper()
	name := "Start at"
	if kind == queues.DateField {
		name = "Date"
	}
	d := &dtRig{}
	d.f = widgets.NewDateTimeField(kind, value, name, nil)
	d.f.Fallback = fallback
	other := widgets.NewButton("Elsewhere", "", widgets.Secondary, widgets.SizeMd, nil)
	d.rig = newRig(t, kit.Size{}, d.f, other)
	d.field = d.host.Fields[0]
	return d
}

// press puts the caret at caret and presses key in the field.
func (d *dtRig) press(key kit.Key, caret int) {
	d.win.Focus(d.f)
	d.field.SetSelection(caret, caret)
	d.key(key, 0)
}

func (d *dtRig) selection() []int {
	s, e := d.field.Selection()
	return []int{s, e}
}

func TestDateTimeFieldKeyboard(t *testing.T) {
	t.Run("is a plain text field in the app's 24-hour format, never the system's picker", func(t *testing.T) {
		d := setupDateTime(t, queues.TimeField, "01:00", "")
		n := node(t, d.f.Base())
		if n.Role != kit.RoleTextField || n.Name != "Start at" || n.Description == "" {
			t.Fatalf("node %v %q %q", n.Role, n.Name, n.Description)
		}
		if d.field.Opts.Placeholder != "hh:mm" || d.field.Kind.MaxLength != 5 {
			t.Errorf("placeholder %q, maxlength %d", d.field.Opts.Placeholder, d.field.Kind.MaxLength)
		}
	})
	t.Run("steps the hour or the minute at the caret with Up, Down, PageUp and PageDown", func(t *testing.T) {
		d := setupDateTime(t, queues.TimeField, "23:30", "")
		d.press(kit.KeyArrowUp, 1)
		if d.f.Text() != "00:30" || !slices.Equal(d.selection(), []int{0, 2}) {
			t.Fatalf("Up: %q %v", d.f.Text(), d.selection())
		}
		d.press(kit.KeyArrowDown, 4)
		if d.f.Text() != "00:29" || !slices.Equal(d.selection(), []int{3, 5}) {
			t.Fatalf("Down: %q %v", d.f.Text(), d.selection())
		}
		d.press(kit.KeyPageUp, 4)
		if d.f.Text() != "00:39" {
			t.Errorf("PageUp: %q", d.f.Text())
		}
		if d.field.Value != d.f.Text() {
			t.Errorf("the native field shows %q", d.field.Value)
		}
	})
	t.Run("starts from the fallback (the start time) when it holds nothing it can read", func(t *testing.T) {
		d := setupDateTime(t, queues.TimeField, "", "22:15")
		d.press(kit.KeyArrowUp, 0)
		if d.f.Text() != "23:15" {
			t.Errorf("%q", d.f.Text())
		}
	})
	t.Run("steps a date by day, month or year, and writes a readable value out in full when left", func(t *testing.T) {
		d := setupDateTime(t, queues.DateField, "2026-10-31", "")
		d.press(kit.KeyArrowUp, 9)
		if d.f.Text() != "2026-11-01" {
			t.Fatalf("Up at the day: %q", d.f.Text())
		}
		d.press(kit.KeyArrowDown, 6)
		if d.f.Text() != "2026-10-01" {
			t.Fatalf("Down at the month: %q", d.f.Text())
		}
		d.field.Replace("2026-1-5")
		d.win.Blur()
		d.settle()
		if d.f.Text() != "2026-01-05" {
			t.Fatalf("left: %q", d.f.Text())
		}
		d.win.Focus(d.f)
		d.field.Replace("2026-02-30") // what it cannot read stays as typed
		d.win.Blur()
		d.settle()
		if d.f.Text() != "2026-02-30" {
			t.Errorf("left: %q", d.f.Text())
		}
	})
	t.Run("leaves Up and Down with a modifier to the system", func(t *testing.T) {
		d := setupDateTime(t, queues.TimeField, "10:00", "")
		d.win.Focus(d.f)
		d.field.SetSelection(1, 1)
		d.key(kit.KeyArrowUp, kit.ModAlt)
		if d.f.Text() != "10:00" {
			t.Errorf("Alt+Up stepped: %q", d.f.Text())
		}
	})
}

func TestNumberFieldAppliesAsTyped(t *testing.T) {
	var applied []string
	n := widgets.NewNumberField("", widgets.InputOptions{Name: "Speed limit", Size: widgets.FieldSm}, func(s string) { applied = append(applied, s) })
	other := widgets.NewButton("Pause", "", widgets.Secondary, widgets.SizeMd, nil)
	r := newRig(t, kit.Size{}, n, other)
	f := r.host.Fields[0]
	r.win.Focus(n)
	f.Type("1")
	f.Type("5")
	r.wait(599e6)
	if len(applied) != 0 {
		t.Fatalf("applied before 600 ms: %q", applied)
	}
	r.wait(2e6)
	if !slices.Equal(applied, []string{"15"}) {
		t.Fatalf("after 600 ms: %q", applied)
	}
	f.Type("0")
	r.key(kit.KeyEnter, 0)
	if !slices.Equal(applied, []string{"15", "150"}) {
		t.Fatalf("Enter: %q", applied)
	}
	r.wait(700e6)
	if len(applied) != 2 {
		t.Fatalf("Enter left a timer: %q", applied)
	}
	f.Type("0")
	r.win.Focus(other)
	r.settle()
	if !slices.Equal(applied, []string{"15", "150", "1500"}) {
		t.Errorf("blur: %q", applied)
	}
}

func TestPasswordInput(t *testing.T) {
	in := widgets.NewInput("", widgets.InputOptions{Name: "Proxy password", Password: true, Size: widgets.FieldSm, Placeholder: "None"}, nil)
	r := newRig(t, kit.Size{}, in)
	f := r.host.Fields[0]
	if f.Kind.Kind != 1 { // kit.FieldPassword
		t.Fatalf("field kind %v", f.Kind.Kind)
	}
	r.win.Focus(in)
	f.Type("s3cret")
	n := node(t, in.Base())
	if n.Value != "" || n.States&kit.NodeProtected == 0 {
		t.Errorf("a password exposed: %q %b", n.Value, n.States)
	}
	if in.Text() != "s3cret" {
		t.Errorf("text %q", in.Text())
	}
}

func TestTextAreaBreaksLinesAndLeavesCtrlEnter(t *testing.T) {
	var submitted int
	a := widgets.NewTextArea("", 4, widgets.InputOptions{Name: "URLs", Placeholder: "https://example.com/file.zip"}, nil)
	d := widgets.NewDialog(widgets.DialogOptions{Title: "Add", OnSubmit: func() { submitted++ }})
	d.Body.Add(a)
	r := newRig(t, kit.Size{})
	d.Show(r.win)
	r.settle()
	f := r.host.Fields[0]
	if f.Kind.Kind != 2 || !f.Shown() { // kit.FieldMultiline
		t.Fatalf("field kind %v shown %v", f.Kind.Kind, f.Shown())
	}
	r.win.Focus(a)
	f.Type("https://a.example/1")
	r.key(kit.KeyEnter, 0)
	f.Type("https://a.example/2")
	if a.Text() != "https://a.example/1\r\nhttps://a.example/2" || submitted != 0 {
		t.Fatalf("text %q, submitted %d", a.Text(), submitted)
	}
	r.key(kit.KeyEnter, kit.ModCtrl)
	if submitted != 0 {
		t.Fatal("the text area submitted on Ctrl+Enter itself (it leaves it to the dialog's owner)")
	}
	// rows: 4 lines of 0.75rem × 1.55 plus 8 px padding above and below and the border
	if h := a.Base().Size().H; h < 4*12*1.55+16+2-0.01 || h > 4*12*1.55+16+2+0.01 {
		t.Errorf("height %v", h)
	}
}

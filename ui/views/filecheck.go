package views

import (
	"downex/ui/kit"
	"downex/ui/kit/tokens"
)

// FileCheckNote is a download's file check note (docs/DESKTOP-SPEC.md §9.7,
// DESIGN-SYSTEM.md §3.21): a fail note for DANGER, a warn note for CAUTION, with the
// heading in bold, then the summary and the detail as paragraphs. The details pane, the
// progress window's Status tab and complete view and the Confirm dialog show it.
type FileCheckNote struct {
	*Note
	Heading, Summary, Detail *Text
}

// NewFileCheckNote makes the note of a check; danger picks the fail tone (else warn).
// detail may be ""; more children (a button) follow the text.
func NewFileCheckNote(heading, summary, detail string, danger bool, more ...kit.Widget) *FileCheckNote {
	tone, ink := NoteWarn, InkWarn
	if danger {
		tone, ink = NoteFail, InkDanger
	}
	strong := FontCell
	strong.Weight = 600
	n := &FileCheckNote{Note: NewNote(tone, "")}
	n.Style.Display = kit.DisplayFlex
	n.Style.Axis = kit.AxisColumn
	n.Style.RowGap = tokens.Sp2
	n.Semantics.Role = kit.RoleAlert
	n.Heading = NewParagraph(heading, strong, ink)
	n.Summary = NewParagraph(summary, FontCell, InkInk2)
	n.Detail = NewParagraph(detail, FontCell, InkInk2)
	n.Detail.SetHidden(detail == "")
	n.Add(n.Heading, n.Summary, n.Detail)
	n.Add(more...)
	return n
}

// SetDanger switches the note between the fail (DANGER) and warn (CAUTION) tones.
func (n *FileCheckNote) SetDanger(danger bool) {
	tone, ink := NoteWarn, InkWarn
	if danger {
		tone, ink = NoteFail, InkDanger
	}
	if n.Tone != tone {
		n.Tone, n.Heading.Ink = tone, ink
		n.Invalidate()
		n.Heading.Invalidate()
	}
}

// Set replaces the note's texts (its tone stays).
func (n *FileCheckNote) Set(heading, summary, detail string) {
	n.Heading.SetText(heading)
	n.Summary.SetText(summary)
	n.Detail.SetText(detail)
	n.Detail.SetHidden(detail == "")
}

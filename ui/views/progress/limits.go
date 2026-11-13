package progress

import (
	"context"
	"strconv"
	"strings"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format"
	"downex/ui/core/validate"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// The Speed limit tab's texts.
const (
	limitToggleText = "Limit this download’s speed"
	limitHintText   = "Applies at once; the global limit in Settings still applies."
	limitNumberText = "Enter a number, such as 500 or 1.5"
	limitAboveText  = "Enter a speed above 0, or turn the limit off"
	connsTitle      = "Changes a running download as soon as it can, at the latest when it starts again"
)

// connsRangeText is "1–32"; connsErrorText the connections field's error.
var (
	connsRangeText = strconv.Itoa(validate.MinConnections) + "–" + strconv.Itoa(validate.MaxConnections)
	connsErrorText = "Enter a whole number from " + strconv.Itoa(validate.MinConnections) + " to " +
		strconv.Itoa(validate.MaxConnections) + ", or leave it empty for the default"
	connsHintText = connsRangeText + ". Changes a running download as soon as it can, at the latest when it starts again."
)

// limitsView is the Speed limit tabpanel (div.limits): the limit's switch, value and unit,
// its error and hint, then the connections with their range and error.
type limitsView struct {
	kit.Element
	toggle     *labelledSwitch
	value      *field
	unit       *widgets.Select
	limitError *views.Text
	hint       *views.Text
	conns      *field
	connsError *views.Text
}

func newLimitsView(s *Screen) *limitsView {
	v := &limitsView{}
	v.Semantics.Role = kit.RoleTabPanel
	v.Style = kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp3}
	setting := func(children ...kit.Widget) *kit.Element {
		return box(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifySpaceBetween,
			ColumnGap: tokens.Sp6, MinHeight: kit.Px(30)}, children...)
	}
	v.toggle = newLabelledSwitch(limitToggleText, s.onLimitToggle)
	v.value = newField(widgets.InputOptions{Name: "Speed limit", Description: limitHintText, Size: widgets.FieldSm},
		s.onLimitInput, s.applySpeed, s.applySpeed)
	v.value.Style.Width = kit.Rem(5.5)
	v.value.Style.Flex = kit.FlexNone
	units := make([]widgets.SelectOption, 0, 2)
	for _, u := range format.RateUnits() {
		units = append(units, widgets.SelectOption{Value: string(u), Label: string(u)})
	}
	v.unit = widgets.NewSelect(units, string(format.KBps), widgets.SelectOptions{AriaLabel: "Speed limit unit", Size: widgets.SelectSm}, s.onUnitChange)
	unitBox := box(kit.Style{Width: kit.Rem(6.25), Flex: kit.FlexNone}, v.unit)
	row := box(kit.Style{Display: kit.DisplayFlex, ColumnGap: tokens.Sp3, Flex: kit.FlexNone}, v.value, unitBox)
	v.limitError = views.NewText("", fontSmall, views.InkDanger, kit.TextOptions{Wrap: kit.TextWrapWords})
	v.limitError.Semantics.Role = kit.RoleAlert
	v.limitError.Semantics.Live = kit.LiveAssertive
	v.limitError.SetHidden(true)
	v.hint = views.NewText(limitHintText, fontSmall, views.InkFaint, kit.TextOptions{Wrap: kit.TextWrapWords})
	v.hint.Style.Margin = kit.Edges{Top: kit.Px(-3), Bottom: kit.Px(-1)} // close to the limit it explains

	label := views.NewText("Connections", fontCell500, views.InkInk, kit.TextOptions{})
	rng := views.NewText(connsRangeText, fontSmall, views.InkFaint, kit.TextOptions{})
	rng.Semantics.Hidden = true
	connsLabel := box(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignBaseline, ColumnGap: tokens.Sp4, Flex: kit.FlexNone}, label, rng)
	v.conns = newField(widgets.InputOptions{Name: "Connections", Description: connsHintText, Size: widgets.FieldSm, Placeholder: "Default", Number: true},
		s.onConnsInput, s.applyConnections, s.applyConnections)
	v.conns.Tooltip = connsTitle
	v.conns.Style.Width = kit.Rem(9.5)
	v.conns.Style.Flex = kit.FlexNone
	v.connsError = views.NewText("", fontSmall, views.InkDanger, kit.TextOptions{Wrap: kit.TextWrapWords})
	v.connsError.Semantics.Role = kit.RoleAlert
	v.connsError.Semantics.Live = kit.LiveAssertive
	v.connsError.SetHidden(true)

	v.Add(setting(v.toggle, row), v.limitError, v.hint, setting(connsLabel, v.conns), v.connsError)
	return v
}

// limits is the state of the Speed limit tab (ProgressWindow.svelte's speed and connection
// limits): the fields, their errors, the typed values waiting to apply, and what the window
// knows of the download's limits.
type limits struct {
	on         bool
	value      string
	unit       format.RateUnit
	limitError string
	connValue  string
	connError  string
	speedTimer *kit.Timer
	connTimer  *kit.Timer
	// sentSpeed and sentConns are the download's limits as far as this window knows (its
	// reports, the answers to what it sent, or what it sent while the answer is
	// outstanding); unknown (false) means the next apply sends.
	sentSpeed      int64
	sentSpeedKnown bool
	sentConns      int32
	sentConnsKnown bool
	// speedEdited and connEdited: the user changed the fields since they last showed the
	// download's limits.
	speedEdited, connEdited bool
	// speedSending and connSending count the calls still waiting for their answer.
	speedSending, connSending int
	// lastSpeed and lastConns are the download's limits the effects last saw.
	lastSpeed, lastConns int64
	sawSpeed, sawConns   bool
}

// editingSpeed: its field or unit has the focus, or a typed value waits to apply.
func (s *Screen) editingSpeed() bool {
	if s.lim.speedTimer.Active() {
		return true
	}
	return s.limitsV != nil && (s.limitsV.value.Focused() || s.limitsV.unit.Focused())
}

func (s *Screen) editingConnections() bool {
	if s.lim.connTimer.Active() {
		return true
	}
	return s.limitsV != nil && s.limitsV.conns.Focused()
}

// showSpeed shows the download's limit in the speed fields.
func (s *Screen) showSpeed(bytes int64) {
	l := &s.lim
	l.sentSpeed, l.sentSpeedKnown = bytes, true
	l.speedEdited = false
	l.limitError = ""
	l.on = bytes > 0
	if bytes > 0 {
		v, u := format.SplitRate(float64(bytes))
		l.value, l.unit = v, u
	}
	s.renderLimits(true)
}

func (s *Screen) showConnections(n int32) {
	l := &s.lim
	l.sentConns, l.sentConnsKnown = n, true
	l.connEdited = false
	l.connError = ""
	l.connValue = ""
	if n > 0 {
		l.connValue = strconv.Itoa(int(n))
	}
	s.renderLimits(true)
}

// followLimits: a limit changed elsewhere ("downex set", another client) shows at once unless
// the user is editing that field; its answer or the end of the edit shows it then.
func (s *Screen) followLimits(d *downexv1.Download) {
	l := &s.lim
	if d == nil {
		l.sawSpeed, l.sawConns = false, false
		return
	}
	if bytes := d.GetSpeedLimit(); !l.sawSpeed || bytes != l.lastSpeed {
		l.sawSpeed, l.lastSpeed = true, bytes
		if l.speedSending == 0 {
			if s.editingSpeed() {
				l.sentSpeed, l.sentSpeedKnown = bytes, true
			} else {
				s.showSpeed(bytes)
			}
		}
	}
	if n := int64(d.GetMaxConnections()); !l.sawConns || n != l.lastConns {
		l.sawConns, l.lastConns = true, n
		if l.connSending == 0 {
			if s.editingConnections() {
				l.sentConns, l.sentConnsKnown = int32(n), true
			} else {
				s.showConnections(int32(n))
			}
		}
	}
}

// wantedSpeed is the speed to apply: 0 = unlimited; false when the field is not valid.
func (s *Screen) wantedSpeed() (int64, bool) {
	l := &s.lim
	if !l.on {
		return 0, true
	}
	loaded := float64(0)
	if l.sentSpeedKnown {
		loaded = float64(l.sentSpeed)
	}
	bytes, ok := format.ParseRate(l.value, l.unit, loaded)
	if !ok {
		l.limitError = limitNumberText
		return 0, false
	}
	if bytes <= 0 {
		l.limitError = limitAboveText
		return 0, false
	}
	l.limitError = ""
	return bytes, true
}

// applySpeed applies an edit of the speed fields; without one, they show the download's limit
// again.
func (s *Screen) applySpeed() {
	l := &s.lim
	l.speedTimer.Stop()
	l.speedTimer = nil
	if !l.speedEdited {
		if d := s.store.Download(); d != nil && l.speedSending == 0 {
			s.showSpeed(d.GetSpeedLimit())
		}
		return
	}
	bytes, ok := s.wantedSpeed()
	s.renderLimits(false)
	if !ok || s.offline() {
		return
	}
	l.speedEdited = false
	if l.sentSpeedKnown && bytes == l.sentSpeed {
		return
	}
	l.sentSpeed, l.sentSpeedKnown = bytes, true
	l.speedSending++
	s.sendOptions(&bytes, nil, func(answer *downexv1.Download) {
		l.speedSending--
		if answer == nil {
			// not applied: the fields keep the edit, and the next apply sends it again
			l.sentSpeedKnown = false
			l.speedEdited = true
			return
		}
		l.sentSpeed, l.sentSpeedKnown = answer.GetSpeedLimit(), true
		if d := s.store.Download(); l.speedSending == 0 && d != nil && !s.editingSpeed() {
			s.showSpeed(d.GetSpeedLimit())
		}
	})
}

// applySpeedSoon applies the typed speed once ApplyDelay has passed without typing.
func (s *Screen) applySpeedSoon() {
	l := &s.lim
	l.speedEdited = true
	l.speedTimer.Stop()
	l.speedTimer = s.pw.Kit.After(s.pw.o.ApplyDelay, s.applySpeed)
}

func (s *Screen) onLimitInput(text string) {
	s.lim.value = text
	s.applySpeedSoon()
}

func (s *Screen) onLimitToggle(on bool) {
	l := &s.lim
	l.on = on
	l.speedEdited = true
	if on && strings.TrimSpace(l.value) == "" {
		l.value, l.unit = "500", format.KBps
	}
	s.renderLimits(true)
	s.applySpeed()
}

func (s *Screen) onUnitChange(v string) {
	s.lim.unit = format.RateUnit(v)
	s.lim.speedEdited = true
	s.applySpeed()
}

// wantedConnections is the number to apply: 0 = the default; false when not valid.
func (s *Screen) wantedConnections() (int32, bool) {
	l := &s.lim
	text := strings.TrimSpace(l.connValue)
	if text == "" {
		l.connError = ""
		return 0, true
	}
	n, err := strconv.Atoi(text)
	digits := text != ""
	for _, r := range text {
		if r < '0' || r > '9' {
			digits = false
		}
	}
	if !digits || err != nil || n < validate.MinConnections || n > validate.MaxConnections {
		l.connError = connsErrorText
		return 0, false
	}
	l.connError = ""
	return int32(n), true
}

func (s *Screen) applyConnections() {
	l := &s.lim
	l.connTimer.Stop()
	l.connTimer = nil
	if !l.connEdited {
		if d := s.store.Download(); d != nil && l.connSending == 0 {
			s.showConnections(d.GetMaxConnections())
		}
		return
	}
	n, ok := s.wantedConnections()
	s.renderLimits(false)
	if !ok || s.offline() {
		return
	}
	l.connEdited = false
	if l.sentConnsKnown && n == l.sentConns {
		return
	}
	l.sentConns, l.sentConnsKnown = n, true
	l.connSending++
	s.sendOptions(nil, &n, func(answer *downexv1.Download) {
		l.connSending--
		if answer == nil {
			l.sentConnsKnown = false
			l.connEdited = true
			return
		}
		// 0 asks for the default: the answer says which number that is.
		l.sentConns, l.sentConnsKnown = answer.GetMaxConnections(), true
		if d := s.store.Download(); l.connSending == 0 && d != nil && !s.editingConnections() {
			s.showConnections(d.GetMaxConnections())
		}
	})
}

func (s *Screen) applyConnectionsSoon() {
	l := &s.lim
	l.connEdited = true
	l.connTimer.Stop()
	l.connTimer = s.pw.Kit.After(s.pw.o.ApplyDelay, s.applyConnections)
}

func (s *Screen) onConnsInput(text string) {
	s.lim.connValue = text
	s.applyConnectionsSoon()
}

// sendOptions sends a change of the download's limits (through act); then gets the answer,
// nil when it failed.
func (s *Screen) sendOptions(speed *int64, conns *int32, then func(*downexv1.Download)) {
	var answer *downexv1.Download
	s.act(func(ctx context.Context) error {
		d, err := s.store.SetOptions(ctx, speed, conns)
		answer = d
		return err
	}, func(ok bool) {
		if !ok {
			answer = nil
		}
		then(answer)
		s.renderLimits(false)
	})
}

// renderLimits shows the limit state in the tab's fields (values only when values is set:
// a field the user types in keeps its text).
func (s *Screen) renderLimits(values bool) {
	v := s.limitsV
	if v == nil {
		return
	}
	l := &s.lim
	off := s.offline()
	v.toggle.sw.SetOn(l.on)
	v.toggle.setDisabled(off)
	if values {
		v.value.SetText(l.value)
		v.unit.SetValue(string(l.unit))
		v.conns.SetText(l.connValue)
	}
	v.value.SetDisabled(!l.on || off)
	v.unit.SetDisabled(!l.on || off)
	v.conns.SetDisabled(off)
	v.value.SetInvalid(l.limitError != "")
	v.conns.SetInvalid(l.connError != "")
	setError(v.limitError, l.limitError)
	setError(v.connsError, l.connError)
	placeholder := "Default"
	if st := s.store.Settings(); st != nil && st.GetDefaultMaxConnections() > 0 {
		placeholder = "Default (" + strconv.Itoa(int(st.GetDefaultMaxConnections())) + ")"
	}
	v.conns.SetPlaceholder(placeholder)
}

func setError(t *views.Text, msg string) {
	t.SetText(msg)
	t.SetHidden(msg == "")
}

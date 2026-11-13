//go:build windows

package uia

import (
	"strings"

	"downex/ui/kit"
	"downex/ui/win/internal/com"
	"downex/ui/win/uia/internal/comobj"
)

// controlType is the UI Automation control type of a role, following the W3C's Core
// Accessibility API Mappings for UIA (and UI Automation's own control type guidance where
// the kit's role has no ARIA twin: the title bar). The root's comes from its window.
func controlType(r kit.Role) int32 {
	switch r {
	case kit.RoleGroup, kit.RoleRegion, kit.RoleNavigation, kit.RoleMain, kit.RoleRadioGroup, kit.RoleRowGroup,
		kit.RoleStatus:
		return UIA_GroupControlTypeId
	case kit.RoleWindow:
		return UIA_WindowControlTypeId
	case kit.RoleTitleBar:
		return UIA_TitleBarControlTypeId
	case kit.RoleToolbar:
		return UIA_ToolBarControlTypeId
	case kit.RoleDialog, kit.RoleAlertDialog, kit.RoleTabPanel:
		return UIA_PaneControlTypeId
	case kit.RoleAlert, kit.RoleText, kit.RoleHeading, kit.RoleTerm, kit.RoleDefinition:
		return UIA_TextControlTypeId
	case kit.RoleTooltip:
		return UIA_ToolTipControlTypeId
	case kit.RoleImage:
		return UIA_ImageControlTypeId
	case kit.RoleButton, kit.RoleSwitch:
		return UIA_ButtonControlTypeId
	case kit.RoleLink:
		return UIA_HyperlinkControlTypeId
	case kit.RoleCheckBox:
		return UIA_CheckBoxControlTypeId
	case kit.RoleRadioButton:
		return UIA_RadioButtonControlTypeId
	case kit.RoleTextField:
		return UIA_EditControlTypeId
	case kit.RoleSpinButton:
		return UIA_SpinnerControlTypeId
	case kit.RoleComboBox:
		return UIA_ComboBoxControlTypeId
	case kit.RoleListBox, kit.RoleList:
		return UIA_ListControlTypeId
	case kit.RoleOption, kit.RoleListItem:
		return UIA_ListItemControlTypeId
	case kit.RoleMenu:
		return UIA_MenuControlTypeId
	case kit.RoleMenuItem, kit.RoleMenuItemCheckBox, kit.RoleMenuItemRadio:
		return UIA_MenuItemControlTypeId
	case kit.RoleSeparator:
		return UIA_SeparatorControlTypeId
	case kit.RoleTabList:
		return UIA_TabControlTypeId
	case kit.RoleTab:
		return UIA_TabItemControlTypeId
	case kit.RoleGrid:
		return UIA_DataGridControlTypeId
	case kit.RoleTable:
		return UIA_TableControlTypeId
	case kit.RoleRow, kit.RoleCell:
		return UIA_DataItemControlTypeId
	case kit.RoleColumnHeader, kit.RoleRowHeader:
		return UIA_HeaderItemControlTypeId
	case kit.RoleProgressBar:
		return UIA_ProgressBarControlTypeId
	case kit.RoleSlider:
		return UIA_SliderControlTypeId
	case kit.RoleScrollBar:
		return UIA_ScrollBarControlTypeId
	case kit.RoleDocument:
		return UIA_DocumentControlTypeId
	}
	return 0
}

// isGrid reports a role with the Grid and Table patterns.
func isGrid(r kit.Role) bool { return r == kit.RoleGrid || r == kit.RoleTable }

// isCell reports a role with the GridItem and TableItem patterns.
func isCell(r kit.Role) bool {
	return r == kit.RoleCell || r == kit.RoleColumnHeader || r == kit.RoleRowHeader
}

// isSelectionContainer reports a role with the Selection pattern.
func isSelectionContainer(r kit.Role) bool {
	switch r {
	case kit.RoleGrid, kit.RoleListBox, kit.RoleTabList, kit.RoleRadioGroup:
		return true
	}
	return false
}

// patternsOf is the set of interfaces a node's provider has (bit per interface index).
func patternsOf(n *kit.SemanticNode, root bool) uint32 {
	m := uint32(1<<ifSimple | 1<<ifFragment)
	if root {
		m |= 1<<ifRoot | 1<<ifHwndOverride
	}
	add := func(k int, on bool) {
		if on {
			m |= 1 << k
		}
	}
	add(ifGrid, isGrid(n.Role))
	add(ifTable, isGrid(n.Role))
	add(ifGridItem, isCell(n.Role))
	add(ifTableItem, isCell(n.Role))
	add(ifSelection, isSelectionContainer(n.Role))
	add(ifSelectionItem, n.States&kit.NodeSelectable != 0 || n.Actions&kit.ActionSelect != 0 ||
		n.Role == kit.RoleOption || n.Role == kit.RoleTab || n.Role == kit.RoleRadioButton)
	menuItem := n.Role == kit.RoleMenuItem || n.Role == kit.RoleMenuItemCheckBox || n.Role == kit.RoleMenuItemRadio
	// A check box, a switch or a toggle button is toggled, not invoked (UI Automation's
	// Button and CheckBox control types); a checkable menu item does both.
	add(ifInvoke, n.Actions&kit.ActionInvoke != 0 && (n.Toggle == kit.ToggleNone || menuItem))
	add(ifValue, n.Role == kit.RoleTextField || n.Role == kit.RoleComboBox || n.Actions&kit.ActionSetValue != 0 ||
		(n.Range.Valid && n.Value != ""))
	add(ifRangeValue, n.Range.Valid)
	add(ifToggle, n.Toggle != kit.ToggleNone)
	add(ifExpandCollapse, n.Expand != kit.ExpandNone)
	add(ifScrollItem, n.Actions&kit.ActionScrollIntoView != 0)
	return m
}

// liveSetting is how a node's changes are announced: its own aria-live, else its role's
// (ARIA: role=alert is assertive, role=status polite).
func liveSetting(n *kit.SemanticNode) int32 {
	switch {
	case n.Live == kit.LiveAssertive:
		return Assertive
	case n.Live == kit.LivePolite:
		return Polite
	case n.Role == kit.RoleAlert:
		return Assertive
	case n.Role == kit.RoleStatus:
		return Polite
	}
	return Off
}

// ---- IRawElementProviderSimple ----------------------------------------------------------

func simpleProviderOptions(this, ret uintptr) uintptr {
	return call(this, func(e *element) uintptr {
		if ret == 0 {
			return hrPointer
		}
		o := int32(ProviderOptions_ServerSideProvider | ProviderOptions_UseComThreading)
		switch {
		case e.override:
			o |= ProviderOptions_OverrideProvider
		case e.root && e.tree.host.CustomFrame():
			o |= ProviderOptions_RefuseNonClientSupport
		}
		*(*int32)(com.Addr(ret)) = o
		return hrOK
	})
}

func simpleGetPatternProvider(this, pattern, ret uintptr) uintptr {
	return call(this, func(e *element) uintptr {
		if ret == 0 {
			return hrPointer
		}
		comobj.SetWord(ret, 0)
		if _, ok := e.node(); !ok {
			return hrNotAvailable
		}
		k := patternIface(int32(uint32(pattern)))
		if k < 0 || e.mask&(1<<k) == 0 {
			return hrOK // not supported: a null pointer
		}
		objects.mu.Lock()
		objects.class.AddRef(e.obj)
		objects.mu.Unlock()
		comobj.SetWord(ret, objects.class.Iface(e.obj, k))
		return hrOK
	})
}

func simpleGetPropertyValue(this, prop, ret uintptr) uintptr {
	return call(this, func(e *element) uintptr {
		if ret == 0 {
			return hrPointer
		}
		v := (*comobj.Variant)(com.Addr(ret))
		*v = comobj.Variant{}
		n, ok := e.node()
		if !ok {
			return hrNotAvailable
		}
		return e.property(&n, int32(uint32(prop)), v)
	})
}

// property writes the value of property id of node n to v, or leaves v VT_EMPTY for a
// property the provider does not give (UI Automation then uses its default, or the
// window's for the root).
func (e *element) property(n *kit.SemanticNode, id int32, v *comobj.Variant) uintptr {
	t := e.tree
	str := func(s string) uintptr {
		if s == "" {
			return hrOK
		}
		b := comobj.NewBSTR(&t.text, s)
		if b == 0 {
			return hrOutOfMemory
		}
		v.SetPointer(comobj.VT_BSTR, b)
		return hrOK
	}
	i4 := func(x int32) uintptr { v.SetI4(x); return hrOK }
	flag := func(b bool) uintptr { v.SetBool(b); return hrOK }
	switch id {
	case UIA_ControlTypePropertyId:
		if e.root {
			return hrOK // the window's: a top-level window
		}
		if ct := controlType(n.Role); ct != 0 {
			return i4(ct)
		}
	case UIA_NamePropertyId:
		if n.Name == "" && !e.root && liveSetting(n) != Off {
			return str(t.liveName(t.tree(), e.id))
		}
		return str(n.Name)
	case UIA_AcceleratorKeyPropertyId:
		return str(n.Shortcut)
	case UIA_AutomationIdPropertyId:
		return str(n.AutomationID)
	case UIA_FullDescriptionPropertyId:
		return str(n.Description)
	case UIA_HasKeyboardFocusPropertyId:
		if !e.root {
			return flag(n.States&kit.NodeFocused != 0)
		}
	case UIA_IsKeyboardFocusablePropertyId:
		if !e.root {
			return flag(n.States&kit.NodeFocusable != 0)
		}
	case UIA_IsEnabledPropertyId:
		return flag(n.States&kit.NodeDisabled == 0)
	case UIA_IsOffscreenPropertyId:
		if !e.root {
			return flag(n.States&kit.NodeOffscreen != 0)
		}
	case UIA_IsPasswordPropertyId:
		if n.States&kit.NodeProtected != 0 {
			return flag(true)
		}
	case UIA_IsRequiredForFormPropertyId:
		if n.States&kit.NodeRequired != 0 {
			return flag(true)
		}
	case UIA_IsDataValidForFormPropertyId:
		if n.States&kit.NodeInvalid != 0 {
			return flag(false)
		}
	case UIA_OrientationPropertyId:
		switch n.Orientation {
		case kit.OrientationHorizontal:
			return i4(OrientationType_Horizontal)
		case kit.OrientationVertical:
			return i4(OrientationType_Vertical)
		}
	case UIA_LiveSettingPropertyId:
		if s := liveSetting(n); s != Off {
			return i4(s)
		}
	case UIA_PositionInSetPropertyId:
		if n.PosInSet > 0 {
			return i4(int32(n.PosInSet))
		}
	case UIA_SizeOfSetPropertyId:
		if n.SetSize > 0 {
			return i4(int32(n.SetSize))
		}
	case UIA_LevelPropertyId:
		if n.Level > 0 && n.Role != kit.RoleHeading {
			return i4(int32(n.Level))
		}
	case UIA_HeadingLevelPropertyId:
		if n.Role == kit.RoleHeading && n.Level >= 1 && n.Level <= 9 {
			return i4(HeadingLevel1 + int32(n.Level) - 1)
		}
	case UIA_LandmarkTypePropertyId:
		switch n.Role {
		case kit.RoleMain:
			return i4(UIA_MainLandmarkTypeId)
		case kit.RoleNavigation:
			return i4(UIA_NavigationLandmarkTypeId)
		}
	case UIA_IsDialogPropertyId:
		if n.Role == kit.RoleDialog || n.Role == kit.RoleAlertDialog {
			return flag(true)
		}
	case UIA_AriaPropertiesPropertyId:
		return str(ariaProperties(n))
	case UIA_ControllerForPropertyId:
		if n.Controls != 0 {
			p := t.iface(n.Controls, ifSimple)
			if p == 0 {
				return hrOK
			}
			psa, err := comobj.NewUnknownArray([]uintptr{p})
			releaseIface(p)
			if err != nil {
				return uintptr(com.Code(err))
			}
			v.SetPointer(comobj.VT_ARRAY|comobj.VT_UNKNOWN, psa)
		}
	}
	return hrOK
}

// ariaProperties is UI Automation's AriaProperties string for what has no property of its
// own: a column header's sort order and a control that opens a popup.
func ariaProperties(n *kit.SemanticNode) string {
	var parts []string
	switch n.Sort {
	case kit.SortAscending:
		parts = append(parts, "sort=ascending")
	case kit.SortDescending:
		parts = append(parts, "sort=descending")
	}
	if n.States&kit.NodeHasPopup != 0 {
		parts = append(parts, "haspopup=true")
	}
	return strings.Join(parts, ";")
}

func simpleHostRawElementProvider(this, ret uintptr) uintptr {
	return call(this, func(e *element) uintptr {
		if ret == 0 {
			return hrPointer
		}
		comobj.SetWord(ret, 0)
		// Only the root is hosted in a window (its HWND properties, frame and Window pattern
		// stay the system's). A hosted child window is placed at its node by the root's
		// GetOverrideProviderForHwnd instead: a node that returned the child's host
		// provider too would show the window twice, once under the root and once nested in
		// itself.
		if !e.root || e.override {
			return hrOK
		}
		p, hr := hostProviderFromHwnd(e.tree.hwnd, e.tree.hostOut)
		if hr.Failed() {
			return uintptr(hr)
		}
		comobj.SetWord(ret, p)
		return hrOK
	})
}

//go:build windows

package uia

// The UI Automation numbers this package uses, named as the Windows SDK names them
// (UIAutomationClient.h, UIAutomationCore.h, UIAutomationCoreApi.h). consts_test.go
// checks every one against the headers.

// Control types (UIA_ControlTypeIds).
const (
	UIA_ButtonControlTypeId      = 50000
	UIA_CheckBoxControlTypeId    = 50002
	UIA_ComboBoxControlTypeId    = 50003
	UIA_EditControlTypeId        = 50004
	UIA_HyperlinkControlTypeId   = 50005
	UIA_ImageControlTypeId       = 50006
	UIA_ListItemControlTypeId    = 50007
	UIA_ListControlTypeId        = 50008
	UIA_MenuControlTypeId        = 50009
	UIA_MenuItemControlTypeId    = 50011
	UIA_ProgressBarControlTypeId = 50012
	UIA_RadioButtonControlTypeId = 50013
	UIA_ScrollBarControlTypeId   = 50014
	UIA_SliderControlTypeId      = 50015
	UIA_SpinnerControlTypeId     = 50016
	UIA_TabControlTypeId         = 50018
	UIA_TabItemControlTypeId     = 50019
	UIA_TextControlTypeId        = 50020
	UIA_ToolBarControlTypeId     = 50021
	UIA_ToolTipControlTypeId     = 50022
	UIA_GroupControlTypeId       = 50026
	UIA_DataGridControlTypeId    = 50028
	UIA_DataItemControlTypeId    = 50029
	UIA_DocumentControlTypeId    = 50030
	UIA_WindowControlTypeId      = 50032
	UIA_PaneControlTypeId        = 50033
	UIA_HeaderItemControlTypeId  = 50035
	UIA_TableControlTypeId       = 50036
	UIA_TitleBarControlTypeId    = 50037
	UIA_SeparatorControlTypeId   = 50038
)

// Properties (UIA_PropertyIds).
const (
	UIA_ControlTypePropertyId                       = 30003
	UIA_NamePropertyId                              = 30005
	UIA_AcceleratorKeyPropertyId                    = 30006
	UIA_HasKeyboardFocusPropertyId                  = 30008
	UIA_IsKeyboardFocusablePropertyId               = 30009
	UIA_IsEnabledPropertyId                         = 30010
	UIA_AutomationIdPropertyId                      = 30011
	UIA_IsControlElementPropertyId                  = 30016
	UIA_IsContentElementPropertyId                  = 30017
	UIA_IsPasswordPropertyId                        = 30019
	UIA_IsOffscreenPropertyId                       = 30022
	UIA_OrientationPropertyId                       = 30023
	UIA_IsRequiredForFormPropertyId                 = 30025
	UIA_ValueValuePropertyId                        = 30045
	UIA_ValueIsReadOnlyPropertyId                   = 30046
	UIA_RangeValueValuePropertyId                   = 30047
	UIA_GridRowCountPropertyId                      = 30062
	UIA_GridColumnCountPropertyId                   = 30063
	UIA_ExpandCollapseExpandCollapseStatePropertyId = 30070
	UIA_SelectionItemIsSelectedPropertyId           = 30079
	UIA_ToggleToggleStatePropertyId                 = 30086
	UIA_AriaPropertiesPropertyId                    = 30102
	UIA_IsDataValidForFormPropertyId                = 30103
	UIA_ControllerForPropertyId                     = 30104
	UIA_LiveSettingPropertyId                       = 30135
	UIA_PositionInSetPropertyId                     = 30152
	UIA_SizeOfSetPropertyId                         = 30153
	UIA_LevelPropertyId                             = 30154
	UIA_LandmarkTypePropertyId                      = 30157
	UIA_FullDescriptionPropertyId                   = 30159
	UIA_HeadingLevelPropertyId                      = 30173
	UIA_IsDialogPropertyId                          = 30174
)

// Control patterns (UIA_PatternIds).
const (
	UIA_InvokePatternId         = 10000
	UIA_SelectionPatternId      = 10001
	UIA_ValuePatternId          = 10002
	UIA_RangeValuePatternId     = 10003
	UIA_ExpandCollapsePatternId = 10005
	UIA_GridPatternId           = 10006
	UIA_GridItemPatternId       = 10007
	UIA_SelectionItemPatternId  = 10010
	UIA_TablePatternId          = 10012
	UIA_TableItemPatternId      = 10013
	UIA_TogglePatternId         = 10015
	UIA_ScrollItemPatternId     = 10017
)

// Events (UIA_EventIds).
const (
	UIA_StructureChangedEventId              = 20002
	UIA_AutomationPropertyChangedEventId     = 20004
	UIA_AutomationFocusChangedEventId        = 20005
	UIA_Invoke_InvokedEventId                = 20009
	UIA_SelectionItem_ElementSelectedEventId = 20012
	UIA_LiveRegionChangedEventId             = 20024
	UIA_NotificationEventId                  = 20035
)

// Landmarks (UIA_LandmarkTypeIds) and heading levels (UIA_HeadingLevelIds).
const (
	UIA_MainLandmarkTypeId       = 80002
	UIA_NavigationLandmarkTypeId = 80003
	HeadingLevel_None            = 80050
	HeadingLevel1                = 80051
)

// ProviderOptions.
const (
	ProviderOptions_ServerSideProvider     = 0x2
	ProviderOptions_OverrideProvider       = 0x8
	ProviderOptions_UseComThreading        = 0x20
	ProviderOptions_RefuseNonClientSupport = 0x40
)

// NavigateDirection.
const (
	NavigateDirection_Parent          = 0
	NavigateDirection_NextSibling     = 1
	NavigateDirection_PreviousSibling = 2
	NavigateDirection_FirstChild      = 3
	NavigateDirection_LastChild       = 4
)

// StructureChangeType.
const (
	StructureChangeType_ChildrenInvalidated = 2
)

// OrientationType.
const (
	OrientationType_None       = 0
	OrientationType_Horizontal = 1
	OrientationType_Vertical   = 2
)

// ExpandCollapseState.
const (
	ExpandCollapseState_Collapsed = 0
	ExpandCollapseState_Expanded  = 1
	ExpandCollapseState_LeafNode  = 3
)

// RowOrColumnMajor.
const (
	RowOrColumnMajor_RowMajor = 0
)

// ToggleState.
const (
	ToggleState_Off           = 0
	ToggleState_On            = 1
	ToggleState_Indeterminate = 2
)

// LiveSetting (its members have no prefix in the header).
const (
	Off       = 0
	Polite    = 1
	Assertive = 2
)

// NotificationKind and NotificationProcessing.
const (
	NotificationKind_Other              = 4
	NotificationProcessing_ImportantAll = 0
	NotificationProcessing_All          = 2
)

// UIAutomationCoreApi.h.
const (
	UiaAppendRuntimeId = 3
	UiaRootObjectId    = -25

	UIA_E_ELEMENTNOTENABLED   = 0x80040200
	UIA_E_ELEMENTNOTAVAILABLE = 0x80040201
	UIA_E_INVALIDOPERATION    = 0x80131509
)

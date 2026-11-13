//go:build windows

// Package uia is the UI Automation provider of ui/win's windows: it shows a window's kit
// semantic tree (kit.SemanticTree) to Narrator, NVDA, Accessibility Insights and every
// other UI Automation client, written from Microsoft's UI Automation provider
// documentation and UIAutomationCore.h.
//
// # What it provides
//
// A window answers WM_GETOBJECT for UiaRootObjectId with its Tree's fragment root
// (Tree.GetObject). Every exposed node of the semantic tree is an element with
// IRawElementProviderSimple and IRawElementProviderFragment; the root also has
// IRawElementProviderFragmentRoot, and its host provider is the window's, so the window's
// own properties (its HWND, process, frame and Window pattern) stay the system's; a window
// whose title bar the root draws refuses the system's non-client provider, whose title bar
// would show it twice. The root's IRawElementProviderHwndOverride places a hosted native
// child window (a text field) at its node, so that it shows once, where the node is. A
// live region without a name of its own (a status bar) is named by the text inside it,
// which is what its change reads out.
//
// The control patterns come from the node: Grid and Table for a grid or table (its rows
// and columns counted over every row of the data, those a virtual list has not laid out
// included, which SemanticTree.Cell makes on demand), GridItem and TableItem for its cells
// and headers, Selection for a grid, list box, tab list or radio group, SelectionItem for
// what can be selected, Invoke for what can be pressed, Value for text fields and combo
// boxes, RangeValue for progress bars, sliders and spin buttons, Toggle for what can be
// checked, ExpandCollapse for what opens something, and ScrollItem for what a scrolling
// ancestor can bring into view. What a client asks a pattern to do becomes a
// kit.ActionRequest that the window performs as a call into its root (Host.Perform);
// Invoke returns at once and performs after the call (Host.Post).
//
// Tree.Notify raises the events of the kit's SemanticEvents: focus changed, structure
// changed (children invalidated), property changed for each property a client saw change,
// live region changed for a live region (aria-live, role=alert, role=status), and
// UiaRaiseNotificationEvent for an announcement.
//
// # Objects and threads
//
// Each provider is a COM object in memory outside the Go heap (internal/comobj) that holds
// a handle into a table, never a Go pointer: a client that keeps an element after its node
// or window is gone gets UIA_E_ELEMENTNOTAVAILABLE, never freed memory. The objects of a
// node are made when a client first asks for it and freed when the last reference goes;
// Live counts them, and returns to 0 once clients release what they hold.
//
// The providers declare ProviderOptions_UseComThreading, and the UI thread is a
// single-threaded apartment, so COM brings every call to the UI thread, where the kit
// lives; a call that arrives elsewhere is refused (Refused counts them) rather than reading
// the tree from another thread. Two methods take doubles, which a Go callback cannot
// receive on x64; small assembly thunks hand them over (thunk_amd64.s). Like the rest of
// ui/win it runs on amd64 only.
package uia

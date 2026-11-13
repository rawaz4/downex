//go:build windows

package uiaclient

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"downex/ui/win/internal/com"
	"downex/ui/win/internal/sdkheader"
)

// TestSlots checks every vtable slot, argument count and interface id of the client
// against UIAutomationClient.h, the handlers' vtables, and the client's CLSID.
func TestSlots(t *testing.T) {
	sdk, ok := sdkheader.Dir()
	if !ok {
		t.Skipf(`Windows SDK headers not found at %s (set DOWNEX_SDK_INCLUDE to the SDK's Include\<version>\um folder)`, sdk)
	}
	found, errs := sdkheader.Check(sdk, sdkheader.Binding{
		Dir:     ".",
		Prefix:  "IUIAutomation",
		Names:   map[string]string{"Automation": "IUIAutomation", "Automation5": "IUIAutomation5"},
		Headers: []string{"UIAutomationClient.h"},
	})
	for _, err := range errs {
		t.Error(err)
	}
	for _, f := range found {
		t.Logf("%-50s %s", f.Where, f.What)
	}
	if len(found) < 40 {
		t.Errorf("only %d bindings checked", len(found))
	}

	set, err := sdkheader.Parse(sdk, "UIAutomationClient.h")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		iface, method string
		fn            any
	}{
		{"IUIAutomationNotificationEventHandler", "HandleNotificationEvent", handleNotification},
		{"IUIAutomationFocusChangedEventHandler", "HandleFocusChangedEvent", handleFocus},
		{"IUIAutomationStructureChangedEventHandler", "HandleStructureChangedEvent", handleStructure},
		{"IUIAutomationEventHandler", "HandleAutomationEvent", handleAutomation},
	} {
		all, err := set.AllMethods(c.iface)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"QueryInterface", "AddRef", "Release", c.method}; !reflect.DeepEqual(all, want) {
			t.Errorf("%s: header %v, handler vtable %v", c.iface, all, want)
		}
		words, err := set.Arity(c.iface, c.method)
		if n := reflect.TypeOf(c.fn).NumIn(); err != nil || n != words {
			t.Errorf("%s::%s: the Go function takes %d words, the header %d (%v)", c.iface, c.method, n, words, err)
		}
	}

	raw, err := os.ReadFile(filepath.Join(sdk, "UIAutomationClient.h"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`DECLSPEC_UUID\("([0-9a-fA-F-]{36})"\)\s*CUIAutomation8;`).FindStringSubmatch(string(raw))
	if m == nil || strings.ToLower(m[1]) != com.GUIDString(clsidCUIAutomation8) {
		t.Errorf("CLSID_CUIAutomation8 %s, header %v", com.GUIDString(clsidCUIAutomation8), m)
	}
}

//go:build windows

package uiaclient

import (
	"sync"
	"syscall"

	"downex/ui/win/internal/com"
	"downex/ui/win/uia/internal/comobj"
)

// Event is what an event handler saw.
type Event struct {
	// Sender is the element's name and control type, read in the handler.
	Name        string
	ControlType int32
	// Text and Activity are a notification's display string and activity id; Kind and
	// Processing its NotificationKind and NotificationProcessing.
	Text, Activity   string
	Kind, Processing int32
	// Change is a structure change's StructureChangeType, RuntimeID its runtime id.
	Change int32
	// EventID is an automation event's id.
	EventID   int32
	RuntimeID []int32
}

// The kinds of handlers.
const (
	notificationHandler = iota
	focusHandler
	structureHandler
	automationHandler
)

// Handler is an event handler object that UI Automation calls on its own threads; each
// event goes to C, and is dropped when C is full.
type Handler struct {
	C    chan Event
	kind int
	obj  uintptr
}

var handlers struct {
	mu      sync.Mutex
	table   comobj.Table[*Handler]
	classes [4]*comobj.Class
	iids    [4]*com.GUID
	once    sync.Once
	err     error
}

func initHandlers() error {
	qi := syscall.NewCallback(handlerQueryInterface)
	addRef := syscall.NewCallback(handlerAddRef)
	release := syscall.NewCallback(handlerRelease)
	for kind, fn := range []any{handleNotification, handleFocus, handleStructure, handleAutomation} {
		vt, err := comobj.Vtable([]uintptr{qi, addRef, release, syscall.NewCallback(fn)})
		if err != nil {
			return err
		}
		handlers.classes[kind] = comobj.NewClass([]uintptr{vt})
	}
	handlers.iids = [4]*com.GUID{
		notificationHandler: &IID_IUIAutomationNotificationEventHandler,
		focusHandler:        &IID_IUIAutomationFocusChangedEventHandler,
		structureHandler:    &IID_IUIAutomationStructureChangedEventHandler,
		automationHandler:   &IID_IUIAutomationEventHandler,
	}
	return nil
}

// NewNotificationHandler, NewFocusHandler, NewStructureHandler and NewAutomationHandler
// make handlers with an event buffer of n; Release drops the caller's reference.
func NewNotificationHandler(n int) (*Handler, error) { return newHandler(notificationHandler, n) }
func NewFocusHandler(n int) (*Handler, error)        { return newHandler(focusHandler, n) }
func NewStructureHandler(n int) (*Handler, error)    { return newHandler(structureHandler, n) }
func NewAutomationHandler(n int) (*Handler, error)   { return newHandler(automationHandler, n) }

func newHandler(kind, n int) (*Handler, error) {
	handlers.once.Do(func() { handlers.err = initHandlers() })
	if handlers.err != nil {
		return nil, handlers.err
	}
	h := &Handler{C: make(chan Event, n), kind: kind}
	handlers.mu.Lock()
	defer handlers.mu.Unlock()
	id := handlers.table.Add(h)
	obj, err := handlers.classes[kind].New(id)
	if err != nil {
		handlers.table.Remove(id)
		return nil, err
	}
	h.obj = obj
	return h, nil
}

func (h *Handler) iface() uintptr { return h.obj }

// Release drops the caller's reference (UI Automation keeps its own until the handler is
// removed).
func (h *Handler) Release() { releaseHandler(h.kind, h.obj) }

// Live is the number of handler objects alive.
func LiveHandlers() int {
	handlers.mu.Lock()
	defer handlers.mu.Unlock()
	return handlers.table.Len()
}

func classOf(this uintptr) (*comobj.Class, int) {
	for k, c := range handlers.classes {
		if c != nil {
			if _, _, ok := c.Object(this); ok {
				return c, k
			}
		}
	}
	return nil, -1
}

func handlerQueryInterface(this, riid, ppv uintptr) uintptr {
	if ppv == 0 {
		return uintptr(com.E_POINTER)
	}
	comobj.SetWord(ppv, 0)
	c, k := classOf(this)
	if c == nil || riid == 0 {
		return uintptr(com.E_NOINTERFACE)
	}
	iid := comobj.GUIDAt(riid)
	if iid != comobj.IID_IUnknown && iid != *handlers.iids[k] {
		return uintptr(com.E_NOINTERFACE)
	}
	handlers.mu.Lock()
	c.AddRef(this)
	handlers.mu.Unlock()
	comobj.SetWord(ppv, this)
	return uintptr(com.S_OK)
}

func handlerAddRef(this uintptr) uintptr {
	c, _ := classOf(this)
	if c == nil {
		return 0
	}
	handlers.mu.Lock()
	defer handlers.mu.Unlock()
	return uintptr(c.AddRef(this))
}

func handlerRelease(this uintptr) uintptr {
	_, k := classOf(this)
	if k < 0 {
		return 0
	}
	return uintptr(releaseHandler(k, this))
}

func releaseHandler(kind int, obj uintptr) int32 {
	c := handlers.classes[kind]
	handlers.mu.Lock()
	defer handlers.mu.Unlock()
	n := c.Release(obj)
	if n == 0 {
		handlers.table.Remove(c.Handle(obj))
		c.Free(obj)
	}
	return n
}

// handlerOf is the Go side of a handler object.
func handlerOf(this uintptr) *Handler {
	c, _ := classOf(this)
	if c == nil {
		return nil
	}
	handlers.mu.Lock()
	defer handlers.mu.Unlock()
	h, _ := handlers.table.Get(c.Handle(this))
	return h
}

// sender reads an event's sender.
func sender(p uintptr) (name string, ct int32) {
	if p == 0 {
		return "", 0
	}
	e := (*Element)(com.Addr(p))
	name, _ = e.Name()
	ct, _ = e.ControlType()
	return name, ct
}

func (h *Handler) send(ev Event) {
	select {
	case h.C <- ev:
	default:
	}
}

// IUIAutomationNotificationEventHandler::HandleNotificationEvent.
func handleNotification(this, src, kind, processing, text, activity uintptr) uintptr {
	if h := handlerOf(this); h != nil {
		name, ct := sender(src)
		h.send(Event{Name: name, ControlType: ct, Text: comobj.BSTRString(text), Activity: comobj.BSTRString(activity),
			Kind: int32(uint32(kind)), Processing: int32(uint32(processing))})
	}
	return uintptr(com.S_OK)
}

// IUIAutomationFocusChangedEventHandler::HandleFocusChangedEvent.
func handleFocus(this, src uintptr) uintptr {
	if h := handlerOf(this); h != nil {
		name, ct := sender(src)
		h.send(Event{Name: name, ControlType: ct})
	}
	return uintptr(com.S_OK)
}

// IUIAutomationStructureChangedEventHandler::HandleStructureChangedEvent.
func handleStructure(this, src, change, rid uintptr) uintptr {
	if h := handlerOf(this); h != nil {
		name, ct := sender(src)
		h.send(Event{Name: name, ControlType: ct, Change: int32(uint32(change)), RuntimeID: comobj.I4Values(rid)})
	}
	return uintptr(com.S_OK)
}

// IUIAutomationEventHandler::HandleAutomationEvent.
func handleAutomation(this, src, id uintptr) uintptr {
	if h := handlerOf(this); h != nil {
		name, ct := sender(src)
		h.send(Event{Name: name, ControlType: ct, EventID: int32(uint32(id))})
	}
	return uintptr(com.S_OK)
}

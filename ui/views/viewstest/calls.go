package viewstest

import (
	"fmt"
	"reflect"
	"slices"

	"downex/ui/views"
)

// Call is one recorded Openers or Actions call: the field's name and its arguments.
type Call struct {
	Name string
	Args []any
}

func (c Call) String() string { return fmt.Sprint(c.Name, c.Args) }

// Calls records the App's Openers and Actions calls, for a screen's tests ("Delete asks to
// remove the selection", "the row's folder button opens its folder").
type Calls struct {
	List []Call
}

// Record fills every Openers and Actions field of a with a function that records its call
// (replacing what was there) and returns the record.
func Record(a *views.App) *Calls {
	c := &Calls{}
	c.fill(reflect.ValueOf(&a.Open).Elem())
	c.fill(reflect.ValueOf(&a.Act).Elem())
	return c
}

func (c *Calls) fill(v reflect.Value) {
	t := v.Type()
	for i := range v.NumField() {
		f := v.Field(i)
		if f.Kind() != reflect.Func {
			continue
		}
		name := t.Field(i).Name
		f.Set(reflect.MakeFunc(f.Type(), func(args []reflect.Value) []reflect.Value {
			call := Call{Name: name}
			for _, a := range args {
				call.Args = append(call.Args, a.Interface())
			}
			c.List = append(c.List, call)
			return nil
		}))
	}
}

// Names are the recorded calls' names in order.
func (c *Calls) Names() []string {
	out := make([]string, len(c.List))
	for i, call := range c.List {
		out[i] = call.Name
	}
	return out
}

// Last is the last call of a name, ok false when there was none.
func (c *Calls) Last(name string) (Call, bool) {
	for i := len(c.List) - 1; i >= 0; i-- {
		if c.List[i].Name == name {
			return c.List[i], true
		}
	}
	return Call{}, false
}

// Has reports a call of the name.
func (c *Calls) Has(name string) bool {
	return slices.ContainsFunc(c.List, func(x Call) bool { return x.Name == name })
}

// Reset forgets the calls.
func (c *Calls) Reset() { c.List = nil }

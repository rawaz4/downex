package pac

import (
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// Argument and date helpers shared by the PAC utility functions.

func argString(call goja.FunctionCall, i int) string {
	v := call.Argument(i)
	if goja.IsUndefined(v) || goja.IsNull(v) {
		return ""
	}
	return v.String()
}

func stringArgs(call goja.FunctionCall) []string {
	out := make([]string, 0, len(call.Arguments))
	for _, a := range call.Arguments {
		if goja.IsUndefined(a) || goja.IsNull(a) {
			continue
		}
		out = append(out, strings.TrimSpace(a.String()))
	}
	return out
}

func intArgs(args []string) ([]int, bool) {
	out := make([]int, 0, len(args))
	for _, a := range args {
		n, err := strconv.Atoi(strings.TrimSpace(a))
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

var months = map[string]time.Month{
	"JAN": time.January, "FEB": time.February, "MAR": time.March, "APR": time.April,
	"MAY": time.May, "JUN": time.June, "JUL": time.July, "AUG": time.August,
	"SEP": time.September, "OCT": time.October, "NOV": time.November, "DEC": time.December,
}

type dateKind int

const (
	dateUnknown dateKind = iota
	dateDay
	dateMonth
	dateYear
)

func classifyDateArgs(args []string) []dateKind {
	kinds := make([]dateKind, len(args))
	for i, a := range args {
		a = strings.ToUpper(strings.TrimSpace(a))
		if _, ok := months[a]; ok {
			kinds[i] = dateMonth
			continue
		}
		if n, err := strconv.Atoi(a); err == nil {
			if n >= 1000 {
				kinds[i] = dateYear
			} else {
				kinds[i] = dateDay
			}
			continue
		}
		kinds[i] = dateUnknown
	}
	return kinds
}

func matchDate(now time.Time, arg string, kind dateKind) bool {
	arg = strings.ToUpper(strings.TrimSpace(arg))
	switch kind {
	case dateDay:
		n, _ := strconv.Atoi(arg)
		return now.Day() == n
	case dateMonth:
		return now.Month() == months[arg]
	case dateYear:
		n, _ := strconv.Atoi(arg)
		return now.Year() == n
	}
	return false
}

func dateBetween(now time.Time, lo, hi string, kind dateKind) bool {
	switch kind {
	case dateDay:
		a, _ := strconv.Atoi(lo)
		b, _ := strconv.Atoi(hi)
		return now.Day() >= a && now.Day() <= b
	case dateMonth:
		a := months[strings.ToUpper(lo)]
		b := months[strings.ToUpper(hi)]
		m := now.Month()
		if a <= b {
			return m >= a && m <= b
		}
		return m >= a || m <= b
	case dateYear:
		a, _ := strconv.Atoi(lo)
		b, _ := strconv.Atoi(hi)
		return now.Year() >= a && now.Year() <= b
	}
	return false
}

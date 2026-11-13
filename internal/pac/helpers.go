package pac

import (
	"net"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// helpers implements the standard PAC utility functions (the Netscape PAC spec). It is
// installed into a runtime by install.
type helpers struct {
	opts Options
	vm   *goja.Runtime
}

func (h *helpers) install(vm *goja.Runtime) error {
	h.vm = vm
	fns := map[string]any{
		"isPlainHostName":     h.isPlainHostName,
		"dnsDomainIs":         h.dnsDomainIs,
		"localHostOrDomainIs": h.localHostOrDomainIs,
		"isResolvable":        h.isResolvable,
		"isInNet":             h.isInNet,
		"dnsResolve":          h.dnsResolve,
		"myIpAddress":         h.myIpAddress,
		"dnsDomainLevels":     h.dnsDomainLevels,
		"shExpMatch":          h.shExpMatch,
		"weekdayRange":        h.weekdayRange,
		"dateRange":           h.dateRange,
		"timeRange":           h.timeRange,
		// Aliases some scripts use.
		"alert": func(goja.FunctionCall) goja.Value { return goja.Undefined() },
	}
	for name, fn := range fns {
		if err := vm.Set(name, fn); err != nil {
			return err
		}
	}
	return nil
}

func (h *helpers) isPlainHostName(host string) bool {
	return !strings.Contains(host, ".")
}

func (h *helpers) dnsDomainIs(host, domain string) bool {
	host = strings.ToLower(host)
	domain = strings.ToLower(domain)
	if host == domain {
		return true
	}
	if !strings.HasPrefix(domain, ".") {
		domain = "." + domain
	}
	return strings.HasSuffix(host, domain)
}

func (h *helpers) localHostOrDomainIs(host, hostdom string) bool {
	host = strings.ToLower(host)
	hostdom = strings.ToLower(hostdom)
	if host == hostdom {
		return true
	}
	// A plain host name (no dot) matches the host part of hostdom.
	if !strings.Contains(host, ".") {
		h2, _, _ := strings.Cut(hostdom, ".")
		return host == h2
	}
	return false
}

func (h *helpers) isResolvable(host string) bool {
	ips, err := h.opts.LookupIP(host)
	return err == nil && len(ips) > 0
}

// dnsResolve returns the IPv4 address of host as text (the first one), null when it
// does not resolve. An IPv4 literal (also IPv4-mapped IPv6) is returned as it is
// without a lookup, as browsers do, so isInNet(dnsResolve(host), ...) works for hosts
// given by address.
func (h *helpers) dnsResolve(host string) goja.Value {
	if host == "" {
		return goja.Null()
	}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return h.vm.ToValue(v4.String())
		}
	}
	ips, err := h.opts.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return goja.Null()
	}
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			return h.vm.ToValue(v4.String())
		}
	}
	return h.vm.ToValue(ips[0].String())
}

func (h *helpers) myIpAddress() string {
	if ip := h.opts.LocalIP(); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
		return ip.String()
	}
	return "127.0.0.1"
}

func (h *helpers) dnsDomainLevels(host string) int {
	return strings.Count(host, ".")
}

func (h *helpers) isInNet(call goja.FunctionCall) goja.Value {
	host := argString(call, 0)
	pattern := argString(call, 1)
	mask := argString(call, 2)
	ip := h.resolveToIPv4(host)
	pat := net.ParseIP(pattern).To4()
	m := net.ParseIP(mask).To4()
	if ip == nil || pat == nil || m == nil {
		return h.vm.ToValue(false)
	}
	for i := 0; i < net.IPv4len; i++ {
		if ip[i]&m[i] != pat[i]&m[i] {
			return h.vm.ToValue(false)
		}
	}
	return h.vm.ToValue(true)
}

// resolveToIPv4 returns host as an IPv4 address: parsed if host is a literal, resolved
// otherwise; nil for an empty host (a failed dnsResolve), without a lookup.
func (h *helpers) resolveToIPv4(host string) net.IP {
	if host == "" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.To4()
	}
	ips, err := h.opts.LookupIP(host)
	if err != nil {
		return nil
	}
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			return v4
		}
	}
	return nil
}

// Limits of shExpMatch's arguments; longer ones never match. Host names and URLs (the
// script gets only the origin of https targets) are far shorter. The helper runs as
// native code that the evaluation timeout cannot interrupt, so one call must stay
// short: with these limits it takes milliseconds at most.
const (
	maxShExpString  = 16 << 10
	maxShExpPattern = 4 << 10
)

// shExpMatch matches str against a shell expression with '*' (any run) and '?' (one
// character), as PAC scripts expect.
func (h *helpers) shExpMatch(str, shexp string) bool {
	if len(str) > maxShExpString || len(shexp) > maxShExpPattern {
		return false
	}
	return shGlob(shexp, str)
}

// shGlob matches s against pattern, where '*' matches any run of bytes (also none) and
// '?' one byte. The pieces between the stars are matched in order, each at its leftmost
// position after the previous one (which never loses a match), with the first piece as a
// prefix and the last as a suffix: the time is linear in len(s) for pieces without '?'
// and at most len(s)*len(piece) for the others, never exponential or quadratic in the
// number of stars.
func shGlob(pattern, s string) bool {
	pieces := strings.Split(pattern, "*")
	if len(pieces) == 1 {
		return matchPiece(pattern, s)
	}
	first, last := pieces[0], pieces[len(pieces)-1]
	if len(s) < len(first)+len(last) ||
		!matchPiece(first, s[:len(first)]) || !matchPiece(last, s[len(s)-len(last):]) {
		return false
	}
	s = s[len(first) : len(s)-len(last)]
	for _, p := range pieces[1 : len(pieces)-1] {
		i := indexPiece(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return true
}

// matchPiece reports whether t matches p exactly, '?' matching any byte.
func matchPiece(p, t string) bool {
	if len(p) != len(t) {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] != '?' && p[i] != t[i] {
			return false
		}
	}
	return true
}

// indexPiece returns the leftmost position at which p ('?' matching any byte) occurs in
// s, or -1.
func indexPiece(s, p string) int {
	if !strings.Contains(p, "?") {
		return strings.Index(s, p)
	}
	for i := 0; i+len(p) <= len(s); i++ {
		if matchPiece(p, s[i:i+len(p)]) {
			return i
		}
	}
	return -1
}

var weekdays = map[string]time.Weekday{
	"SUN": time.Sunday, "MON": time.Monday, "TUE": time.Tuesday, "WED": time.Wednesday,
	"THU": time.Thursday, "FRI": time.Friday, "SAT": time.Saturday,
}

// weekdayRange(wd1 [, wd2] [, "GMT"]) reports whether today is in the range.
func (h *helpers) weekdayRange(call goja.FunctionCall) goja.Value {
	args := stringArgs(call)
	gmt := false
	if len(args) > 0 && strings.EqualFold(args[len(args)-1], "GMT") {
		gmt = true
		args = args[:len(args)-1]
	}
	if len(args) == 0 {
		return h.vm.ToValue(false)
	}
	now := h.opts.Now()
	if gmt {
		now = now.UTC()
	} else {
		now = now.Local()
	}
	today := now.Weekday()
	wd1, ok1 := weekdays[strings.ToUpper(args[0])]
	if !ok1 {
		return h.vm.ToValue(false)
	}
	if len(args) == 1 {
		return h.vm.ToValue(today == wd1)
	}
	wd2, ok2 := weekdays[strings.ToUpper(args[1])]
	if !ok2 {
		return h.vm.ToValue(false)
	}
	return h.vm.ToValue(weekdayInRange(today, wd1, wd2))
}

func weekdayInRange(today, from, to time.Weekday) bool {
	if from <= to {
		return today >= from && today <= to
	}
	// Range wraps across the week (e.g. FRI..MON).
	return today >= from || today <= to
}

// dateRange supports the common forms: a single day, a day range, month names and
// four-digit years, each optionally followed by "GMT". Unsupported argument shapes
// return false.
func (h *helpers) dateRange(call goja.FunctionCall) goja.Value {
	args := stringArgs(call)
	gmt := false
	if len(args) > 0 && strings.EqualFold(args[len(args)-1], "GMT") {
		gmt = true
		args = args[:len(args)-1]
	}
	if len(args) == 0 {
		return h.vm.ToValue(false)
	}
	now := h.opts.Now()
	if gmt {
		now = now.UTC()
	} else {
		now = now.Local()
	}
	// Classify each argument as day (1..31), month (JAN..DEC) or year (>=1000).
	kinds := classifyDateArgs(args)
	switch {
	case len(args) == 1:
		return h.vm.ToValue(matchDate(now, args[0], kinds[0]))
	case len(args) == 2:
		return h.vm.ToValue(matchDate(now, args[0], kinds[0]) || matchDate(now, args[1], kinds[1]) ||
			dateBetween(now, args[0], args[1], kinds[0]))
	}
	return h.vm.ToValue(false)
}

// timeRange(h1,m1,s1,h2,m2,s2 | h1,h2 | h1 [,"GMT"]) is approximated for the hour and
// the full h:m:s range forms.
func (h *helpers) timeRange(call goja.FunctionCall) goja.Value {
	raw := stringArgs(call)
	gmt := false
	if len(raw) > 0 && strings.EqualFold(raw[len(raw)-1], "GMT") {
		gmt = true
		raw = raw[:len(raw)-1]
	}
	now := h.opts.Now()
	if gmt {
		now = now.UTC()
	} else {
		now = now.Local()
	}
	nums, ok := intArgs(raw)
	if !ok || len(nums) == 0 {
		return h.vm.ToValue(false)
	}
	secOfDay := now.Hour()*3600 + now.Minute()*60 + now.Second()
	switch len(nums) {
	case 1:
		return h.vm.ToValue(now.Hour() == nums[0])
	case 2:
		return h.vm.ToValue(inRange(now.Hour(), nums[0], nums[1]))
	case 4:
		from := nums[0]*3600 + nums[1]*60
		to := nums[2]*3600 + nums[3]*60
		return h.vm.ToValue(inRange(secOfDay, from, to))
	case 6:
		from := nums[0]*3600 + nums[1]*60 + nums[2]
		to := nums[3]*3600 + nums[4]*60 + nums[5]
		return h.vm.ToValue(inRange(secOfDay, from, to))
	}
	return h.vm.ToValue(false)
}

func inRange(v, from, to int) bool {
	if from <= to {
		return v >= from && v <= to
	}
	return v >= from || v <= to
}

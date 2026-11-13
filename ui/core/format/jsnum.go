package format

import (
	"math"
	"math/big"
	"strconv"
	"strings"
)

// The arithmetic and number-to-text rules of the TypeScript this package ports, so that a
// template like `${n} B` reads the same in Go.

// Round is JavaScript's Math.round: the nearest integer, halves toward +∞ (-2.5 → -2).
func Round(x float64) float64 { return jsRound(x) }

// Num is JavaScript's String(number): the shortest text that reads back as x, in plain
// digits from 1e-7 up to 1e21 and in exponent form ("1e+21", "1.5e-7") beyond; "NaN",
// "Infinity", "-Infinity"; -0 is "0".
func Num(x float64) string { return jsString(x) }

// Fixed is JavaScript's Number.prototype.toFixed(digits) for 0 ≤ digits ≤ 100: x's exact
// value rounded to digits decimals, halves away from zero; String(x) from 1e21 on.
func Fixed(x float64, digits int) string { return jsFixed(x, digits) }

func jsRound(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}
	r := math.Floor(x)
	if x-r >= 0.5 {
		r++
	}
	if r == 0 && math.Signbit(x) {
		return math.Copysign(0, -1)
	}
	return r
}

func jsString(x float64) string {
	switch {
	case math.IsNaN(x):
		return "NaN"
	case math.IsInf(x, 1):
		return "Infinity"
	case math.IsInf(x, -1):
		return "-Infinity"
	case x == 0:
		return "0"
	}
	sign := ""
	if x < 0 {
		sign = "-"
		x = -x
	}
	// The shortest digits and their exponent: "d.ddde±XX".
	e := strconv.FormatFloat(x, 'e', -1, 64)
	mant, expText, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	exp, _ := strconv.Atoi(expText)
	k := len(digits)
	n := exp + 1 // the position of the decimal point after the first n digits
	var b strings.Builder
	b.WriteString(sign)
	switch {
	case k <= n && n <= 21:
		b.WriteString(digits)
		b.WriteString(strings.Repeat("0", n-k))
	case 0 < n && n <= 21:
		b.WriteString(digits[:n])
		b.WriteByte('.')
		b.WriteString(digits[n:])
	case -6 < n && n <= 0:
		b.WriteString("0.")
		b.WriteString(strings.Repeat("0", -n))
		b.WriteString(digits)
	default:
		b.WriteByte(digits[0])
		if k > 1 {
			b.WriteByte('.')
			b.WriteString(digits[1:])
		}
		b.WriteByte('e')
		if n-1 >= 0 {
			b.WriteByte('+')
		}
		b.WriteString(strconv.Itoa(n - 1))
	}
	return b.String()
}

func jsFixed(x float64, digits int) string {
	digits = max(0, min(digits, 100))
	if math.IsNaN(x) {
		return "NaN"
	}
	if math.Abs(x) >= 1e21 || math.IsInf(x, 0) {
		return jsString(x)
	}
	sign := ""
	if x < 0 {
		sign = "-"
		x = -x
	}
	// Every double has a finite decimal expansion; 1100 digits hold any of them.
	exact := new(big.Float).SetFloat64(x).Text('f', 1100)
	intPart, frac, _ := strings.Cut(exact, ".")
	keep := []byte(intPart + frac[:digits])
	if frac[digits] >= '5' {
		i := len(keep) - 1
		for ; i >= 0; i-- {
			if keep[i] == '9' {
				keep[i] = '0'
				continue
			}
			keep[i]++
			break
		}
		if i < 0 {
			keep = append([]byte{'1'}, keep...)
		}
	}
	n := len(keep) - digits
	out := string(keep[:n])
	if digits > 0 {
		out += "." + string(keep[n:])
	}
	// a negative number that rounds to zero keeps its sign ("-0.0"), as toFixed does
	return sign + out
}

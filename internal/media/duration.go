package media

import (
	"errors"
	"strings"
	"time"
)

// parseISODuration parses the xs:duration values of an MPD (ISO 8601 durations such
// as "PT1H2M3.5S" or "P0Y0M1DT0H0M0.000S"). Days count 24 hours; non-zero years or
// months have no fixed length and are refused, as are negative durations. Only the
// seconds may have a fraction; precision beyond a nanosecond is truncated.
func parseISODuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "P") || len(s) < 2 {
		return 0, errors.New("not an ISO 8601 duration")
	}
	s = s[1:]
	var total time.Duration
	inTime, seen, seenSeconds := false, false, false
	order := 0 // components must appear in order: Y M D | H M S
	for len(s) > 0 {
		if s[0] == 'T' {
			if inTime || len(s) == 1 {
				return 0, errors.New("misplaced T in ISO 8601 duration")
			}
			inTime, order = true, 0
			s = s[1:]
			continue
		}
		if seenSeconds {
			return 0, errors.New("ISO 8601 duration continues after seconds")
		}
		i := 0
		for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
			i++
		}
		if i == 0 || i == len(s) {
			return 0, errors.New("malformed ISO 8601 duration")
		}
		num, designator := s[:i], s[i]
		s = s[i+1:]
		var rank int
		var unit time.Duration
		switch {
		case !inTime && designator == 'Y':
			rank = 1
		case !inTime && designator == 'M':
			rank = 2
		case !inTime && designator == 'D':
			rank, unit = 3, 24*time.Hour
		case inTime && designator == 'H':
			rank, unit = 1, time.Hour
		case inTime && designator == 'M':
			rank, unit = 2, time.Minute
		case inTime && designator == 'S':
			rank, unit, seenSeconds = 3, time.Second, true
		default:
			return 0, errors.New("unknown ISO 8601 duration designator")
		}
		if rank <= order {
			return 0, errors.New("ISO 8601 duration components out of order")
		}
		order = rank
		seen = true
		if strings.Contains(num, ".") && unit != time.Second {
			return 0, errors.New("fraction in an ISO 8601 duration component other than seconds")
		}
		if unit == time.Second {
			d, err := parseSeconds(num)
			if err != nil {
				return 0, err
			}
			total = addDuration(total, d)
			continue
		}
		v, err := parseDecimalInt(num)
		if err != nil {
			return 0, err
		}
		if unit == 0 { // years or months
			if v != 0 {
				return 0, errors.New("ISO 8601 duration in years or months has no fixed length")
			}
			continue
		}
		if v > uint64(maxDuration/unit) {
			return 0, errors.New("ISO 8601 duration out of range")
		}
		total = addDuration(total, time.Duration(v)*unit)
	}
	if !seen {
		return 0, errors.New("empty ISO 8601 duration")
	}
	if total == maxDuration {
		return 0, errors.New("ISO 8601 duration out of range")
	}
	return total, nil
}

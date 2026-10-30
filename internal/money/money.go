// Package money handles BDT amounts stored as int64 poisha (1 taka = 100 poisha).
package money

import (
	"errors"
	"strconv"
	"strings"
)

var ErrInvalid = errors.New("invalid amount")

// Format renders poisha as "৳1,250" or "৳1,250.50".
func Format(p int64) string {
	neg := p < 0
	if neg {
		p = -p
	}
	taka, paisa := p/100, p%100
	s := groupThousands(strconv.FormatInt(taka, 10))
	if paisa != 0 {
		s += "." + twoDigits(paisa)
	}
	if neg {
		return "-৳" + s
	}
	return "৳" + s
}

// Plain renders poisha as an unformatted decimal string ("1250.50"), for form inputs and CSV.
func Plain(p int64) string {
	s := strconv.FormatInt(p/100, 10)
	if r := p % 100; r != 0 {
		if r < 0 {
			r = -r
		}
		s += "." + twoDigits(r)
	}
	return s
}

// Parse converts "1250", "1,250.5" or "1250.50" into poisha. Negative values are rejected.
func Parse(s string) (int64, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	s = strings.TrimPrefix(s, "৳")
	if s == "" {
		return 0, ErrInvalid
	}
	whole, frac, hasFrac := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	t, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || t < 0 {
		return 0, ErrInvalid
	}
	var p int64
	if hasFrac {
		if len(frac) == 0 || len(frac) > 2 {
			return 0, ErrInvalid
		}
		if len(frac) == 1 {
			frac += "0"
		}
		p, err = strconv.ParseInt(frac, 10, 64)
		if err != nil || p < 0 {
			return 0, ErrInvalid
		}
	}
	if t > (1<<62)/100 {
		return 0, ErrInvalid
	}
	return t*100 + p, nil
}

func twoDigits(n int64) string {
	if n < 10 {
		return "0" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}

// groupThousands uses Western grouping (1,234,567).
func groupThousands(s string) string {
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

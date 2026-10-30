// Package phone validates and normalises Bangladeshi mobile numbers.
package phone

import (
	"errors"
	"regexp"
	"strings"
)

var ErrInvalid = errors.New("enter a valid Bangladeshi mobile number (e.g. 01712345678)")

// 11 digits, operator prefixes 013–019.
var bdMobile = regexp.MustCompile(`^01[3-9][0-9]{8}$`)

// Normalize accepts 01XXXXXXXXX, 8801XXXXXXXXX or +8801XXXXXXXXX (with optional
// spaces/dashes) and returns the canonical 11-digit local form.
func Normalize(s string) (string, error) {
	s = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '(', ')', '.':
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "+")
	if strings.HasPrefix(s, "880") {
		s = s[2:] // 8801XXXXXXXXX -> 01XXXXXXXXX
	}
	if !bdMobile.MatchString(s) {
		return "", ErrInvalid
	}
	return s, nil
}

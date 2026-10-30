// Package slug builds URL-safe slugs.
package slug

import (
	"strings"
	"unicode"
)

// Make lowercases s and replaces runs of non-alphanumeric characters with a single dash.
// Non-ASCII letters (e.g. Bangla) are kept, since browsers handle them in URLs.
func Make(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

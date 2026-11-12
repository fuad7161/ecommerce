package accounts

import "testing"

func TestNormalizeEmail(t *testing.T) {
	for in, want := range map[string]bool{
		"  Rahim@Example.com ":   true,
		"rahim@example":          false,
		"Rahim <rahim@x.com>":    false,
		"not-an-email":           false,
		"rahim.uddin@mail.co.bd": true,
	} {
		if _, ok := normalizeEmail(in); ok != want {
			t.Errorf("normalizeEmail(%q) ok=%v, want %v", in, ok, want)
		}
	}
	if got, _ := normalizeEmail("  Rahim@Example.com "); got != "rahim@example.com" {
		t.Errorf("not lower-cased/trimmed: %q", got)
	}
}

func TestPasswordRules(t *testing.T) {
	fe := FieldErrors{}
	checkPassword(fe, "p", "short")
	checkPassword(fe, "q", string(make([]byte, 73)))
	checkPassword(fe, "ok", "long-enough")
	if fe["p"] == "" || fe["q"] == "" || fe["ok"] != "" {
		t.Fatalf("unexpected errors: %v", fe)
	}
}

package phone

import "testing"

func TestNormalize(t *testing.T) {
	ok := map[string]string{
		"01712345678":      "01712345678",
		"+8801712345678":   "01712345678",
		"8801912345678":    "01912345678",
		"017-1234-5678":    "01712345678",
		" 01312 345 678 ":  "01312345678",
		"+880 1512-345678": "01512345678",
	}
	for in, want := range ok {
		got, err := Normalize(in)
		if err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}

	bad := []string{
		"", "0171234567", "017123456789", "01212345678", // 012 is not a mobile prefix
		"01112345678", "1712345678", "+8811712345678", "0171234567a", "02-9123456",
	}
	for _, in := range bad {
		if _, err := Normalize(in); err == nil {
			t.Errorf("Normalize(%q) expected error", in)
		}
	}
}

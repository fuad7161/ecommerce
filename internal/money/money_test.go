package money

import "testing"

func TestFormat(t *testing.T) {
	cases := map[int64]string{
		0:         "৳0",
		7000:      "৳70",
		125050:    "৳1,250.50",
		100000000: "৳1,000,000",
		-5000:     "-৳50",
	}
	for in, want := range cases {
		if got := Format(in); got != want {
			t.Errorf("Format(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestParse(t *testing.T) {
	ok := map[string]int64{
		"1250":    125000,
		"1,250.5": 125050,
		"1250.50": 125050,
		"৳70":     7000,
		" 0.99 ":  99,
		".5":      50,
	}
	for in, want := range ok {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "-1", "abc", "1.234", "1.", "12a"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) expected error", bad)
		}
	}
}

func TestPlainRoundTrip(t *testing.T) {
	for _, p := range []int64{0, 5, 100, 125050, 99999} {
		got, err := Parse(Plain(p))
		if err != nil || got != p {
			t.Errorf("round trip %d -> %q -> %d (%v)", p, Plain(p), got, err)
		}
	}
}

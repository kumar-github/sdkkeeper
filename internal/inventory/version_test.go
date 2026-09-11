package inventory

import "testing"

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int // sign only: negative, zero, positive
	}{
		{"8.0", "11.0.15", -1},
		{"11.0.15", "17.0.3", -1},
		{"17.0.3", "21.0.2", -1},
		{"21.0.2", "25.0.1", -1},
		{"21.0.2", "21.0.2", 0},
		{"21.0.2", "21.0.1", 1},
		{"25.0.1", "8.0", 1},
		// Regression cases for a real bug caught via actual use:
		// vendor-suffixed version labels (real, installed data --
		// e.g. "26.0.1-liberica") broke the old, pure-integer
		// component parsing. The exact reported scenario: with two
		// Liberica installs differing only in patch version, the
		// suffix on the last dot-component made both compare as
		// equal, so the picker showed 26.0.1 before 26.0.2 --
		// backwards, and not a real sort at all, just whatever order
		// the filesystem happened to return them in.
		{"26.0.1-liberica", "26.0.2-liberica", -1},
		{"26.0.2-liberica", "26.0.1-liberica", 1},
		{"23.0.2-temurin", "16.0.2-temurin", 1},
		{"21.0.2", "23.0.2-temurin", -1}, // bare vs. suffixed, still compares numerically
		{"21.0.9-temurin", "21.0.9-temurin", 0},
	}

	for _, c := range cases {
		got := CompareVersions(c.a, c.b)
		gotSign := sign(got)
		if gotSign != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d (sign %d), want sign %d", c.a, c.b, got, gotSign, c.want)
		}
	}
}

func sign(n int) int {
	if n < 0 {
		return -1
	}
	if n > 0 {
		return 1
	}
	return 0
}

func TestLeadingInt(t *testing.T) {
	cases := map[string]int{
		"26":         26,
		"1-liberica": 1,
		"2-liberica": 2,
		"0":          0,
		"":           0,
		"-liberica":  0, // no leading digits at all
	}
	for in, want := range cases {
		got := leadingInt(in)
		if got != want {
			t.Errorf("leadingInt(%q) = %d, want %d", in, got, want)
		}
	}
}

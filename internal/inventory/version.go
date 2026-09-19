package inventory

import (
	"strconv"
	"strings"
)

// CompareVersions does a numeric, dot-separated comparison, e.g.
// "8.0" < "11.0.15" < "17.0.3" < "21.0.2" < "25.0.1". Parses only the
// leading digit run of each component (via leadingInt), not requiring
// the whole component to be a pure integer -- otherwise a vendor
// suffix on the last component (e.g. "1-liberica") would make
// strconv.Atoi fail and default to 0, comparing different versions as
// equal.
func CompareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var na, nb int
		if i < len(pa) {
			na = leadingInt(pa[i])
		}
		if i < len(pb) {
			nb = leadingInt(pb[i])
		}
		if na != nb {
			return na - nb
		}
	}
	// Numerically identical -- fall back to a plain string comparison
	// as a deterministic tiebreaker, rather than relying on
	// sort.SliceStable's input-order behavior for equal elements.
	return strings.Compare(a, b)
}

// leadingInt parses the leading digit run of s as an integer,
// stopping at the first non-digit character, e.g. "1-liberica" -> 1.
func leadingInt(s string) int {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, _ := strconv.Atoi(s[:end])
	return n
}

package inventory

import "strconv"
import "strings"

// CompareVersions does a numeric, dot-separated comparison, e.g.
// "8.0" < "11.0.15" < "17.0.3" < "21.0.2" < "25.0.1". Originally ported
// directly from the PoC's compareVersions, unchanged for bare version
// numbers -- but a real bug was found via actual use once
// vendor-suffixed labels (e.g. "26.0.1-liberica") became real,
// installed data: the last dot-component of a suffixed label isn't a
// pure integer ("1-liberica"), so strconv.Atoi silently failed and
// defaulted to 0 for EVERY such component -- meaning "26.0.1-liberica"
// and "26.0.2-liberica" compared as EQUAL, and sort.SliceStable just
// preserved whatever order the filesystem happened to return them in
// (fragile and non-deterministic -- OS/filesystem dependent, not a
// real ordering guarantee). Fixed by parsing only the LEADING digit
// run of each component, stopping at the first non-digit character,
// rather than requiring the whole component to already be a pure
// integer.
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
	// Numerically identical (a genuine tie, or the same version
	// number from two different vendors) -- fall back to a plain
	// string comparison as a final, deterministic tiebreaker, rather
	// than depending on sort.SliceStable's "preserve input order"
	// behavior for equal elements, which silently depends on
	// whatever order the filesystem happened to iterate entries in.
	return strings.Compare(a, b)
}

// leadingInt parses the leading digit run of s as an integer,
// stopping at the first non-digit character -- e.g. "1-liberica" -> 1,
// "2-liberica" -> 2 -- rather than requiring the WHOLE string to
// already be a pure integer, which is what silently broke the moment
// a vendor suffix like "-liberica" got appended to the last
// dot-component of a version label.
func leadingInt(s string) int {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, _ := strconv.Atoi(s[:end])
	return n
}

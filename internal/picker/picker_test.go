package picker

import (
	"os"
	"strings"
	"testing"

	"sdkkeeper/internal/term"
)

// TestWindowBounds_ShortListReturnsFullRange is the key safety
// guarantee for this whole fix: whenever the list already fits
// (itemCount <= maxVisible), windowBounds returns (0, itemCount)
// unconditionally -- the SAME full range View() always used before
// windowing existed at all. This is what makes the "don't break the
// existing, stable picker" requirement a provable property of the
// windowing function itself, not just an assumption about View()'s
// surrounding code.
func TestWindowBounds_ShortListReturnsFullRange(t *testing.T) {
	cases := []struct{ cursor, itemCount, maxVisible int }{
		{0, 3, 10},
		{2, 3, 10},
		{0, 5, 5}, // exact fit
		{4, 5, 5},
		{0, 1, 10},
	}
	for _, c := range cases {
		start, end := windowBounds(c.cursor, c.itemCount, c.maxVisible)
		if start != 0 || end != c.itemCount {
			t.Errorf("windowBounds(%d, %d, %d) = (%d, %d), want (0, %d)",
				c.cursor, c.itemCount, c.maxVisible, start, end, c.itemCount)
		}
	}
}

func TestWindowBounds_CursorAtTopShowsFirstItems(t *testing.T) {
	start, end := windowBounds(0, 20, 5)
	if start != 0 || end != 5 {
		t.Errorf("windowBounds(0, 20, 5) = (%d, %d), want (0, 5)", start, end)
	}
}

func TestWindowBounds_CursorAtBottomShowsLastItems(t *testing.T) {
	start, end := windowBounds(19, 20, 5)
	if start != 15 || end != 20 {
		t.Errorf("windowBounds(19, 20, 5) = (%d, %d), want (15, 20)", start, end)
	}
}

func TestWindowBounds_CursorInMiddleIsRoughlyCentered(t *testing.T) {
	start, end := windowBounds(10, 20, 5)
	if start > 10 || end <= 10 {
		t.Fatalf("windowBounds(10, 20, 5) = (%d, %d) -- cursor 10 must be within the returned window", start, end)
	}
	// Cursor should land near the middle of the window, not pinned
	// to either edge.
	offsetFromStart := 10 - start
	if offsetFromStart < 1 || offsetFromStart > 3 {
		t.Errorf("windowBounds(10, 20, 5): cursor is %d rows from window start, expected roughly centered (1-3)", offsetFromStart)
	}
}

func TestWindowBounds_ZeroOrNegativeMaxVisibleReturnsFullRange(t *testing.T) {
	start, end := windowBounds(5, 20, 0)
	if start != 0 || end != 20 {
		t.Errorf("windowBounds with maxVisible=0 = (%d, %d), want (0, 20) -- must not panic or clamp to an empty range", start, end)
	}
}

func TestWindowBounds_NeverExceedsItemBounds(t *testing.T) {
	// A broad sweep, not just hand-picked cases -- confirms start/end
	// always stay within [0, itemCount] regardless of cursor position,
	// including cursor values that would be invalid in practice (defensive,
	// since a future caller bug elsewhere could still pass one).
	itemCount, maxVisible := 20, 5
	for cursor := -5; cursor <= 25; cursor++ {
		start, end := windowBounds(cursor, itemCount, maxVisible)
		if start < 0 || end > itemCount || start > end {
			t.Errorf("windowBounds(%d, %d, %d) = (%d, %d) -- out of valid bounds", cursor, itemCount, maxVisible, start, end)
		}
	}
}

func TestMaxVisibleItems_NeverBelowMinimum(t *testing.T) {
	for _, h := range []int{-10, 0, 5, 9, 10, 11} {
		if got := maxVisibleItems(h); got < 3 {
			t.Errorf("maxVisibleItems(%d) = %d, want >= 3", h, got)
		}
	}
}

// buildStyles gets a real, usable term.Styles without needing an
// actual terminal session -- StylesForWriter builds one directly
// against any *os.File (see its own doc comment in term/style.go).
// os.Stderr is used purely as a real, always-available file handle
// for color-detection purposes; nothing in these tests actually
// writes to it -- only View()'s own returned string is inspected.
func buildStyles(t *testing.T) term.Styles {
	t.Helper()
	return term.StylesForWriter(os.Stderr)
}

// TestView_ShortListUnaffectedByWindowing is the direct, rendered-
// output regression test for the same safety guarantee
// TestWindowBounds_ShortListReturnsFullRange proves at the pure-
// function level: a short list's rendered View() output must be
// IDENTICAL whether height is unknown (m.height == 0, the pre-
// windowing default) or a real, known height is set -- confirming
// the windowing code path this fix adds never actually changes
// anything for the case that was already working.
func TestView_ShortListUnaffectedByWindowing(t *testing.T) {
	styles := buildStyles(t)
	items := []string{"21.0.2-temurin", "17.0.3-temurin", "11.0.2-temurin"}

	withoutHeight := model{title: "Select JDK version", rows: rowsFromItems(items), styles: styles}
	withHeight := model{title: "Select JDK version", rows: rowsFromItems(items), styles: styles, width: 80, height: 40}

	out1 := withoutHeight.View()
	out2 := withHeight.View()

	// lipgloss.Place (only reached when width/height are both set)
	// pads the box to fill the full terminal area, so the two
	// outputs won't be byte-identical strings -- but the actual BOX
	// CONTENT (title, items, no scroll indicators) must be the same
	// in both. Checked by confirming neither scroll indicator glyph
	// appears, and every item is present, in both renders.
	for _, out := range []string{out1, out2} {
		if strings.Contains(out, "more above") || strings.Contains(out, "more below") {
			t.Errorf("a short list must never show scroll indicators, got:\n%s", out)
		}
		for _, item := range items {
			if !strings.Contains(out, item) {
				t.Errorf("expected item %q to be visible, got:\n%s", item, out)
			}
		}
	}
}

// TestView_LongListShowsWindowAndIndicators confirms the actual fix:
// a list taller than the available height now shows a bounded
// window, with scroll indicators signaling hidden content, instead
// of silently overflowing past the screen edge.
func TestView_LongListShowsWindowAndIndicators(t *testing.T) {
	styles := buildStyles(t)
	items := make([]string, 30)
	for i := range items {
		items[i] = "8.0." + string(rune('a'+i)) + "-temurin"
	}

	m := model{title: "Select JDK version", rows: rowsFromItems(items), cursor: 0, styles: styles, width: 80, height: 20}
	out := m.View()

	if !strings.Contains(out, "more below") {
		t.Error("expected a 'more below' indicator when the cursor is at the top of a long list")
	}
	if strings.Contains(out, "more above") {
		t.Error("did not expect a 'more above' indicator when the cursor is at the very top")
	}
	// The title must still be visible -- the exact thing the
	// reported bug said was scrolled off-screen.
	if !strings.Contains(out, "Select JDK version") {
		t.Error("expected the title to be visible even for a long list")
	}
}

func TestView_LongListCursorAtBottomShowsAboveIndicator(t *testing.T) {
	styles := buildStyles(t)
	items := make([]string, 30)
	for i := range items {
		items[i] = "8.0." + string(rune('a'+i)) + "-temurin"
	}

	m := model{title: "Select JDK version", rows: rowsFromItems(items), cursor: 29, styles: styles, width: 80, height: 20}
	out := m.View()

	if !strings.Contains(out, "more above") {
		t.Error("expected a 'more above' indicator when the cursor is at the bottom of a long list")
	}
	if strings.Contains(out, "more below") {
		t.Error("did not expect a 'more below' indicator when the cursor is at the very bottom")
	}
}

// TestMoveCursor_NeverLandsOnHeader is a real, live-caught regression
// test: an earlier version of this exact logic (built and verified in
// a standalone POC before landing here) could return a header's own
// index at the boundary, once that header had been visited as an
// intermediate step during skipping.
func TestMoveCursor_NeverLandsOnHeader(t *testing.T) {
	rows := rowsFromGroups([]Group{
		{Header: "Temurin", Items: []string{"21.0.2-temurin", "17.0.3-temurin"}},
		{Header: "Liberica", Items: []string{"26.0.2-liberica", "18.0.1.1-liberica", "17.0.16-liberica"}},
	})
	cursor := firstSelectable(rows)
	if rows[cursor].isHeader {
		t.Fatalf("firstSelectable landed on a header")
	}
	for i := 0; i < len(rows)+5; i++ {
		cursor = moveCursor(rows, cursor, 1)
		if rows[cursor].isHeader {
			t.Fatalf("cursor landed on header at index %d after moving down", cursor)
		}
	}
	for i := 0; i < len(rows)+5; i++ {
		cursor = moveCursor(rows, cursor, -1)
		if rows[cursor].isHeader {
			t.Fatalf("cursor landed on header at index %d after moving up", cursor)
		}
	}
}

func TestMoveCursor_ClampsAtBoundaries(t *testing.T) {
	rows := rowsFromGroups([]Group{
		{Header: "Temurin", Items: []string{"21.0.2-temurin", "17.0.3-temurin"}},
		{Header: "Liberica", Items: []string{"26.0.2-liberica"}},
	})
	first := firstSelectable(rows)
	if got := moveCursor(rows, first, -1); got != first {
		t.Errorf("expected clamp at first selectable row %d, got %d", first, got)
	}
	last := len(rows) - 1
	if got := moveCursor(rows, last, 1); got != last {
		t.Errorf("expected clamp at last selectable row %d, got %d", last, got)
	}
}

// TestMoveCursor_NoHeadersDegeneratesToSingleStep confirms the exact
// claim moveCursor's own doc comment makes: with zero header rows,
// this behaves identically to the package's original, pre-header
// single-step up/down -- not just "still works", but takes exactly
// one step per call, the same as before.
func TestMoveCursor_NoHeadersDegeneratesToSingleStep(t *testing.T) {
	rows := rowsFromItems([]string{"a", "b", "c", "d"})
	if got := moveCursor(rows, 1, 1); got != 2 {
		t.Errorf("moveCursor(rows, 1, 1) = %d, want 2 (single step, no headers to skip)", got)
	}
	if got := moveCursor(rows, 1, -1); got != 0 {
		t.Errorf("moveCursor(rows, 1, -1) = %d, want 0", got)
	}
}

// TestRunGrouped_SingleFlatGroupEquivalentToRun confirms RunGrouped's
// own doc-comment claim: a single group with no header produces
// EXACTLY the same rows Run would for the same items -- the
// mechanism `RunGrouped(sess, title, []Group{{Items: items}},
// banner)` reduces to plain Run for every existing single-vendor
// caller (install's own three picker call sites stay on plain Run
// unchanged; this confirms the two are genuinely equivalent for any
// caller that DID migrate).
func TestRunGrouped_SingleFlatGroupEquivalentToRun(t *testing.T) {
	items := []string{"3.9.9", "3.9.6", "3.8.8"}
	fromRun := rowsFromItems(items)
	fromGroups := rowsFromGroups([]Group{{Items: items}})

	if len(fromRun) != len(fromGroups) {
		t.Fatalf("row count differs: Run-style %d, RunGrouped-style %d", len(fromRun), len(fromGroups))
	}
	for i := range fromRun {
		if fromRun[i] != fromGroups[i] {
			t.Errorf("row %d differs: Run-style %+v, RunGrouped-style %+v", i, fromRun[i], fromGroups[i])
		}
	}
}

func TestRowsFromGroups_HeaderRowsPrecedeTheirItems(t *testing.T) {
	rows := rowsFromGroups([]Group{
		{Header: "Temurin", Items: []string{"21.0.2-temurin", "17.0.3-temurin"}},
		{Header: "Liberica", Items: []string{"26.0.2-liberica"}},
	})
	want := []row{
		{isHeader: true, label: "Temurin"},
		{label: "21.0.2-temurin"},
		{label: "17.0.3-temurin"},
		{isHeader: true, label: "Liberica"},
		{label: "26.0.2-liberica"},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

func TestRowsFromGroups_EmptyHeaderProducesNoHeaderRow(t *testing.T) {
	rows := rowsFromGroups([]Group{{Header: "", Items: []string{"a", "b"}}})
	for _, r := range rows {
		if r.isHeader {
			t.Errorf("expected no header row for an empty Header, got: %+v", rows)
		}
	}
	if len(rows) != 2 {
		t.Errorf("expected 2 item rows, got %d: %+v", len(rows), rows)
	}
}

// TestView_GroupedRendersHeadersAndSkipsThemOnMovement is the direct,
// rendered-output confirmation of the header feature end to end: both
// headers appear in the box, and after moving the cursor past the
// first group's last item, it lands on the SECOND group's first item,
// never on the "Liberica" header row in between.
func TestView_GroupedRendersHeadersAndSkipsThemOnMovement(t *testing.T) {
	styles := buildStyles(t)
	rows := rowsFromGroups([]Group{
		{Header: "Temurin", Items: []string{"21.0.2-temurin", "17.0.3-temurin"}},
		{Header: "Liberica", Items: []string{"26.0.2-liberica"}},
	})
	m := model{title: "Select JDK version", rows: rows, cursor: firstSelectable(rows), styles: styles}

	out := m.View()
	if !strings.Contains(out, "Temurin") || !strings.Contains(out, "Liberica") {
		t.Errorf("expected both group headers visible, got:\n%s", out)
	}

	// Move past both Temurin items -- should land on the Liberica
	// item, not the Liberica header.
	m.cursor = moveCursor(m.rows, m.cursor, 1) // -> 17.0.3-temurin
	m.cursor = moveCursor(m.rows, m.cursor, 1) // -> skip "Liberica" header -> 26.0.2-liberica
	if m.rows[m.cursor].label != "26.0.2-liberica" {
		t.Errorf("expected cursor to land on 26.0.2-liberica after skipping the header, landed on %q", m.rows[m.cursor].label)
	}
}

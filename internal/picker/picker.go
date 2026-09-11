// Package picker provides a single, reusable interactive version-picker
// used by every tool's `use` command -- not reimplemented per tool the
// way the original shell scripts effectively were. Deliberately generic:
// it knows nothing about Java, Maven, or any other tool, only that it's
// showing a list of strings (optionally grouped under header rows) and
// returning whichever one was chosen.
package picker

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"sdkkeeper/internal/term"
)

// ErrNoTTY is returned when Run is called without a real terminal
// available (term.Session.HasTTY == false). Deliberately explicit
// rather than silently doing nothing -- design doc §6.4 documents at
// length what happens when a picker fails to render with no clear
// signal as to why; this package refuses to repeat that failure mode.
var ErrNoTTY = errors.New("picker: no real terminal available (/dev/tty could not be opened)")

// Group is a named cluster of selectable items, shown under its own
// non-selectable header row -- e.g. one vendor's versions under that
// vendor's name, matching the same grouping `sk list` already shows
// as plain text. Header == "" means "no header row for this group" --
// its items still appear, just without a heading above them; RunGrouped
// with every group's Header empty is equivalent to Run.
type Group struct {
	Header string
	Items  []string
}

// row is either a non-selectable group header or a selectable item --
// the two are flattened into one ordered slice internally, since they
// share the same list/scroll/cursor logic; only rendering and cursor
// movement need to tell them apart.
type row struct {
	isHeader bool
	label    string
}

func rowsFromItems(items []string) []row {
	rows := make([]row, len(items))
	for i, it := range items {
		rows[i] = row{label: it}
	}
	return rows
}

func rowsFromGroups(groups []Group) []row {
	var rows []row
	for _, g := range groups {
		if g.Header != "" {
			rows = append(rows, row{isHeader: true, label: g.Header})
		}
		for _, it := range g.Items {
			rows = append(rows, row{label: it})
		}
	}
	return rows
}

func firstSelectable(rows []row) int {
	for i, r := range rows {
		if !r.isHeader {
			return i
		}
	}
	return 0
}

type model struct {
	banner string // already-styled context text shown ABOVE the box, e.g.
	// a "No JDK selected" prompt or a prior step's confirmation --
	// see Run's doc comment for why this exists.
	title    string
	rows     []row
	cursor   int
	chosen   string
	quitting bool
	width    int
	height   int
	styles   term.Styles
}

func (m model) Init() tea.Cmd { return nil }

// moveCursor moves the cursor by delta rows, SKIPPING over header
// rows entirely -- a header is never a valid cursor position. Clamps
// at the first/last selectable row rather than wrapping.
//
// With no headers at all (every row selectable, the common case --
// most tool/vendor selections in this project have exactly one
// vendor), this degenerates to the exact same single-step up/down
// this package always did: the loop's first candidate is immediately
// non-header, so it returns on the very first iteration every time.
// That's what keeps this a genuinely additive change for every
// existing, already-stable flat-list caller, not just a design goal.
func moveCursor(rows []row, cursor, delta int) int {
	valid := cursor // last known non-header position -- what gets
	// returned if we run off the edge before finding another
	// selectable row. A real bug caught and fixed while building and
	// verifying this exact logic in an earlier standalone POC:
	// returning the loop's scratch variable directly at the boundary
	// could return a HEADER's own index, once that header had been
	// visited as an intermediate step, rather than falling back to
	// the last genuinely valid position. Covered by
	// TestMoveCursor_ClampsAtBoundaries below.
	next := cursor
	for {
		candidate := next + delta
		if candidate < 0 || candidate >= len(rows) {
			return valid
		}
		next = candidate
		if !rows[next].isHeader {
			return next
		}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			m.cursor = moveCursor(m.rows, m.cursor, -1)
		case "down", "j":
			m.cursor = moveCursor(m.rows, m.cursor, 1)
		case "enter":
			m.chosen = m.rows[m.cursor].label
			return m, tea.Quit
		}
	}
	return m, nil
}

// windowBounds computes which slice of rows [start, end) should be
// visible, given the cursor's current position, the total row count,
// and how many rows are actually available to show them in. A pure
// function of those three inputs -- NOT incrementally-adjusted state
// tracked across Update calls -- deliberately: the window is
// recomputed fresh from the cursor's position on every single render,
// so there is no separate "window state" that could ever drift out of
// sync with where the cursor actually is.
//
// A real, reported bug this exists to fix: with no windowing at all,
// a list taller than the terminal simply rendered past the bottom
// edge, silently scrolling the title and top items off-screen with no
// indication anything was missing.
//
// Keeps the cursor roughly centered within the window as it moves,
// clamping at the top/bottom edges of the full list -- the same
// general behavior most real terminal pickers (fzf and similar) use.
//
// Header rows are just ordinary rows to this function -- not pinned
// or treated specially -- deliberately: a header can scroll off with
// its group like any other row, relying on the scroll indicators
// above/below to signal there's more content, rather than a more
// complex "sticky header" mechanism. Evaluated directly, interactively,
// in an earlier standalone POC before this landed here.
//
// When rowCount already fits within maxVisible (the common case for
// this project's own real lists -- most tool/vendor selections are
// short), returns (0, rowCount) unconditionally: every row,
// unwindowed. This is deliberately the exact same result the old,
// pre-windowing code always produced, for exactly the cases that
// already worked correctly -- see View's own comment on why this
// specific property is what keeps this change safe for existing,
// already-stable behavior.
func windowBounds(cursor, rowCount, maxVisible int) (start, end int) {
	if maxVisible <= 0 || rowCount <= maxVisible {
		return 0, rowCount
	}
	start = cursor - maxVisible/2
	if start < 0 {
		start = 0
	}
	end = start + maxVisible
	if end > rowCount {
		end = rowCount
		start = end - maxVisible
	}
	return start, end
}

// maxVisibleItems estimates how many ROWS fit in the available
// terminal height, after reserving space for the box's own border and
// padding (4 lines: top border, top padding, bottom padding, bottom
// border -- see Styles.Box's own construction), the title line, and a
// margin for an optional banner. Deliberately a generous, approximate
// reservation rather than an exact character-by-character accounting:
// UNDER-counting available space (showing a few fewer rows than
// would technically fit) is a harmless, purely cosmetic cost:
// OVER-counting (claiming more room than genuinely exists) would
// reproduce the exact overflow bug this whole mechanism exists to
// prevent.
func maxVisibleItems(height int) int {
	const overhead = 10
	const minVisible = 3
	visible := height - overhead
	if visible < minVisible {
		return minVisible
	}
	return visible
}

func (m model) View() string {
	if m.chosen != "" || m.quitting {
		// Deliberately blank: no in-picker confirmation text here.
		// resolveUse owns ALL user-facing confirmation/cancellation
		// messages now, printed once cleanly after this alt-screen
		// program has fully exited -- see the design note in Run's
		// doc comment for why duplicating that here caused problems.
		return ""
	}

	var b strings.Builder
	if m.banner != "" {
		b.WriteString(m.banner)
		b.WriteString("\n\n")
	}
	b.WriteString(m.styles.Title.Render(m.title))
	b.WriteString("\n")

	// start/end default to the FULL row list -- matching, byte for
	// byte, what this function always did before windowing (and
	// before headers) existed at all. Only narrowed when height is
	// actually known (m.height > 0, i.e. at least one real
	// WindowSizeMsg has arrived) AND the list genuinely doesn't fit --
	// see windowBounds' own doc comment for why the untouched, "fits
	// already" case is guaranteed to return these exact same
	// (0, len(rows)) bounds anyway.
	start, end := 0, len(m.rows)
	if m.height > 0 {
		start, end = windowBounds(m.cursor, len(m.rows), maxVisibleItems(m.height))
	}

	if start > 0 {
		b.WriteString(m.styles.Detail.Render(fmt.Sprintf("  \u2191 %d more above", start)))
		b.WriteString("\n")
	}
	for i := start; i < end; i++ {
		r := m.rows[i]
		switch {
		case r.isHeader:
			// Same style `sk list`'s own "Managed by SDK Keeper:" /
			// vendor sub-headings already use outside the picker --
			// a header means the same thing in both places, so it
			// gets the same color.
			b.WriteString(m.styles.Header.Render(r.label))
		case i == m.cursor:
			b.WriteString(m.styles.Active.Render("\u279c   " + r.label))
		default:
			b.WriteString(m.styles.Inactive.Render("    " + r.label))
		}
		b.WriteString("\n")
	}
	if end < len(m.rows) {
		b.WriteString(m.styles.Detail.Render(fmt.Sprintf("  \u2193 %d more below", len(m.rows)-end)))
		b.WriteString("\n")
	}

	box := m.styles.Box.Render(strings.TrimRight(b.String(), "\n"))
	if m.width > 0 && m.height > 0 {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
	}
	return box
}

func run(sess *term.Session, title string, rows []row, banner string) (string, error) {
	if !sess.HasTTY {
		return "", ErrNoTTY
	}

	m := model{
		banner: banner,
		title:  title,
		rows:   rows,
		cursor: firstSelectable(rows),
		styles: sess.Styles(),
	}

	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithOutput(sess.Out), tea.WithInput(sess.In))
	result, err := p.Run()
	if err != nil {
		return "", err
	}

	final := result.(model)
	return final.chosen, nil
}

// Run shows an interactive, arrow-key-only picker for items, titled
// with title (e.g. "Select JDK version"). Returns the chosen item, or
// ("", nil) if the user cancelled (Esc/Ctrl-C) -- never an error for a
// deliberate cancel, matching the fail-fast-but-not-punishing philosophy
// used throughout this project. Returns ErrNoTTY if sess has no real
// terminal to render to.
//
// banner is optional (pass "" for none): already-styled context text
// rendered ABOVE the box, INSIDE the same alt-screen frame as the
// picker itself. This exists specifically to fix a real UX bug: printing
// a status line (e.g. "No JDK selected — choose one first:") and then
// immediately launching an alt-screen picker causes that status line to
// be wiped out a fraction of a second after it appears, since alt-screen
// clears and switches buffers. Folding that text into the picker's own
// render, as a banner, means it's never printed-then-immediately-erased
// -- it's part of one coherent screen. Callers (resolveUse) are
// responsible for deciding what banner text to pass and when.
func Run(sess *term.Session, title string, items []string, banner string) (string, error) {
	return run(sess, title, rowsFromItems(items), banner)
}

// RunGrouped is Run, but items are shown clustered under named,
// non-selectable header rows -- for callers presenting versions from
// more than one vendor at once (`sk use`/`sk remove` with no version
// given), where a flat list would otherwise interleave different
// vendors' versions with no indication which is which. A group with
// Header == "" contributes its items with no header line above them.
// RunGrouped(sess, title, []Group{{Items: items}}, banner) is
// equivalent to Run(sess, title, items, banner).
func RunGrouped(sess *term.Session, title string, groups []Group, banner string) (string, error) {
	return run(sess, title, rowsFromGroups(groups), banner)
}

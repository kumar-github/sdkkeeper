// Package picker provides a single, reusable interactive version
// picker used by every tool's `use` command. Deliberately generic: it
// knows nothing about Java, Maven, or any other tool, only that it's
// showing a list of strings (optionally grouped under headers) and
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
// available (term.Session.HasTTY == false).
var ErrNoTTY = errors.New("picker: no real terminal available (/dev/tty could not be opened)")

// Group is a named cluster of selectable items shown under its own
// non-selectable header row -- e.g. one vendor's versions under its
// name. Header == "" means no header row; RunGrouped with every
// group's Header empty is equivalent to Run.
type Group struct {
	Header string
	Items  []string
}

// row is either a non-selectable group header or a selectable item,
// flattened into one ordered slice since both share list/scroll/
// cursor logic; only rendering and cursor movement distinguish them.
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
	banner   string // already-styled context shown above the box; see Run
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

// moveCursor moves the cursor by delta rows, skipping header rows
// entirely -- a header is never a valid cursor position. Clamps at
// the first/last selectable row rather than wrapping. With no headers
// at all, this degenerates to a plain single-step up/down.
func moveCursor(rows []row, cursor, delta int) int {
	valid := cursor // last known non-header position, for the boundary case
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
// visible, given the cursor position, total row count, and available
// height. A pure function of those three inputs, recomputed fresh on
// every render, so there's no separate window state to drift out of
// sync. Keeps the cursor roughly centered as it moves, clamping at
// the list's edges (like fzf and similar pickers). Header rows are
// ordinary rows here -- not pinned -- so a header can scroll off with
// its group, relying on the scroll indicators to signal more content.
// When rowCount already fits within maxVisible, returns (0, rowCount)
// unconditionally: every row, unwindowed.
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

// maxVisibleItems estimates how many rows fit in the available
// terminal height, reserving space for the box border/padding, title,
// and an optional banner. Deliberately a generous, approximate
// reservation: undercounting only costs a few rows cosmetically;
// overcounting would reproduce the overflow bug this exists to fix.
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
		// Blank: resolveUse owns all confirmation/cancellation
		// messages, printed once after this alt-screen program exits.
		return ""
	}

	var b strings.Builder
	if m.banner != "" {
		b.WriteString(m.banner)
		b.WriteString("\n\n")
	}
	b.WriteString(m.styles.Title.Render(m.title))
	b.WriteString("\n")

	// Defaults to the full row list; narrowed only once height is
	// known (a real WindowSizeMsg has arrived) and doesn't fit.
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
			// Same style `sk list`'s own headers use outside the picker.
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
// with title. Returns the chosen item, or ("", nil) if the user
// cancelled (Esc/Ctrl-C) -- never an error for a deliberate cancel.
// Returns ErrNoTTY if sess has no real terminal to render to.
//
// banner is optional (pass "" for none): already-styled context text
// rendered above the box, inside the same alt-screen frame. This
// avoids a status line being wiped out a moment after printing, right
// before an alt-screen picker launches -- folding it into the
// picker's own render makes it part of one coherent screen instead.
func Run(sess *term.Session, title string, items []string, banner string) (string, error) {
	return run(sess, title, rowsFromItems(items), banner)
}

// RunGrouped is Run, but items are clustered under named,
// non-selectable header rows -- for presenting versions from more
// than one vendor at once, where a flat list would otherwise
// interleave them with no indication which is which.
func RunGrouped(sess *term.Session, title string, groups []Group, banner string) (string, error) {
	return run(sess, title, rowsFromGroups(groups), banner)
}

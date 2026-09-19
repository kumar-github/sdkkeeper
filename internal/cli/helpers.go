package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/spf13/cobra"

	"sdkkeeper/internal/inventory"
	"sdkkeeper/internal/term"
	"sdkkeeper/internal/tooldef"
)

// requireArgs wraps a cobra.PositionalArgs validator, printing a
// styled "usage: sk <cmd.Use>" message before returning the error --
// without this, root.go's SilenceErrors means the user sees nothing
// at all for a missing or extra argument.
//
// Cannot use the package-level session/styles here: cobra validates
// Args before PersistentPreRunE runs, the only place they're
// initialized, so session is still nil at this point.
//
// Uses term.StylesForWriter(os.Stderr), not term.Open() -- term.Open
// writes to /dev/tty whenever a real terminal is available, which
// would send this message there instead of os.Stderr on a real
// machine (invisible to a sandbox with no real terminal to bypass).
// StylesForWriter builds color detection against os.Stderr directly,
// the writer this actually prints to.
func requireArgs(validator cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := validator(cmd, args); err != nil {
			s := term.StylesForWriter(os.Stderr)
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, s.Error.Render(fmt.Sprintf("\u2717 usage: sk %s", cmd.Use)))
			return err
		}
		return nil
	}
}

// requireTool looks up a tool by name, printing the standard "unknown
// tool" error and returning a non-nil error if it doesn't exist.
// Shared by every command that needs this same first check
// (add, current, default, install, list, remove, search, vendors).
func requireTool(toolName string) (tooldef.Tool, error) {
	tool, ok := tooldef.Get(toolName)
	if !ok {
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf("\u2717 unknown tool: %s", toolName)))
		return tooldef.Tool{}, fmt.Errorf("unknown tool: %s", toolName)
	}
	return tool, nil
}

// resolveSymlinkPath returns target's resolved location if it's a
// symlink (or target unchanged if not, or if resolution fails).
func resolveSymlinkPath(target string) string {
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if real, err := os.Readlink(target); err == nil {
			return real
		}
	}
	return target
}

// displayPath returns the path to SHOW the user for a Version.
// v.Path stores the symlink's own location for external (add-
// registered) entries, which is right for activation but wrong for
// display -- the user wants to see the real, registered location,
// not sk's internal bookkeeping path.
func displayPath(v inventory.Version) string {
	if !v.External {
		return v.Path
	}
	return resolveSymlinkPath(v.Path)
}

// indentLines prepends indent to every line of s, dropping trailing
// blank lines -- used after renderTable, which has no indentation
// concept of its own.
func indentLines(s, indent string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(indent)
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// renderTable renders rows as an aligned, borderless table, shared by
// list.go and search.go. styleFunc may be nil; rows[i][j] must be
// plain text, never pre-styled/ANSI-colored -- lipgloss/table
// miscalculates column widths otherwise. Its StyleFunc row index is
// offset by 1 from the data (row 0 is always an internal header row),
// handled here so callers can index by data position directly.
// Without an explicit Width(), table auto-detects terminal width,
// which collapses every column outside a real TTY (e.g. piped/
// redirected output) -- so width is always passed explicitly.
//
// tableWidth computes that content-driven width, with a safety
// margin (undershooting truncates real data; overshooting only adds
// whitespace), exposed separately so multiple renderTable calls
// (e.g. one per vendor sub-group in list.go) can share one consistent
// width instead of each sizing independently -- otherwise two
// side-by-side tables with different content lengths visibly
// misalign.
func tableWidth(rows [][]string, numCols int) int {
	widths := make([]int, numCols)
	for _, r := range rows {
		for c, cell := range r {
			if len(cell) > widths[c] {
				widths[c] = len(cell)
			}
		}
	}
	total := 0
	for _, w := range widths {
		total += w
	}
	return total + numCols*4 + 4 // safety margin -- see renderTable's own doc comment
}

func renderTable(rows [][]string, numCols int, width int, styleFunc func(row, col int) lipgloss.Style) string {
	t := table.New().
		Border(lipgloss.Border{}).
		BorderTop(false).
		BorderBottom(false).
		BorderLeft(false).
		BorderRight(false).
		BorderColumn(false).
		Width(width)
	// BorderHeader stays at its default (true): disabling BorderTop,
	// BorderBottom, AND BorderHeader together silently drops the last
	// data row in this pinned lipgloss/table version (v0.11.0). No
	// visible side effect since row 0 is an unused header row anyway.

	if styleFunc != nil {
		t.StyleFunc(func(row, col int) lipgloss.Style {
			if row == 0 {
				return lipgloss.NewStyle() // the always-present, unused header row
			}
			return styleFunc(row-1, col)
		})
	}
	for _, r := range rows {
		t.Row(r...)
	}
	return t.String()
}

// clearDefaultFile removes tool's default-version file, and the
// shared defaults/ directory too if now empty. Shared by `sk default
// <tool> null` and `sk remove` (when the removed version was the
// default). Idempotent: returns nil if the file was already gone.
func clearDefaultFile(tool tooldef.Tool) error {
	err := os.Remove(tool.DefaultPath())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	dir := filepath.Dir(tool.DefaultPath())
	if entries, readErr := os.ReadDir(dir); readErr == nil && len(entries) == 0 {
		os.Remove(dir)
	}
	return nil
}

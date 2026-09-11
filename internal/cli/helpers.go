package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/spf13/cobra"

	"sdkkeeper/internal/inventory"
	"sdkkeeper/internal/term"
	"sdkkeeper/internal/tooldef"
)

// requireArgs wraps a cobra.PositionalArgs validator, printing a
// clear, properly-styled "usage: sk <cmd.Use>" message before
// returning the error -- otherwise root.go's SilenceErrors means the
// user sees absolutely nothing at all for something as simple as a
// missing or extra argument. A real bug caught via actual use, and
// confirmed to affect EVERY command in this project, not just the one
// it was first reported on -- every command used a bare cobra.Args
// validator with no such printing. Reads cmd.Use directly rather than
// taking a separate usage string, since cobra already has the exact
// right text there for every command.
//
// Cannot use the package-level session/styles here -- a real crash
// caught via actual testing: cobra validates Args BEFORE running
// PersistentPreRunE, which is the ONLY place session/styles ever get
// initialized (see root.go's own comment on why). session is
// genuinely still nil at the point this runs, so using it directly
// panics with a nil pointer dereference on every single invocation
// with a bad arg count.
//
// A real, separate inconsistency found via actual use, fixed here
// too: this message carries the SAME "✗" glyph every other genuine
// failure in this project uses, styled red via Styles.Error -- but,
// for a while, was left completely unstyled (plain text), the only
// "✗" message in the whole project that didn't actually render red.
//
// Deliberately uses term.StylesForWriter(os.Stderr), NOT term.Open()
// -- a real, SECOND bug in that first fix, caught via actual testing
// on a REAL MACHINE (this project's own sandbox has no real terminal
// at all, so it never caught this): term.Open() correctly opens
// /dev/tty and writes there whenever a real terminal IS available --
// which meant this message went to /dev/tty, not os.Stderr, on any
// real machine. Still genuinely visible to the user, but a test using
// a standard os.Stderr redirect could never observe it, and had only
// been passing in this sandbox by coincidence (no real /dev/tty here
// to bypass anything with). StylesForWriter builds color detection
// directly against os.Stderr -- the exact writer this actually prints
// to -- keeping the message both correctly styled AND reliably
// testable regardless of whether a real terminal happens to be
// attached.
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
//
// Extracted after a clean-code pass found this exact two-line
// print-then-error block duplicated verbatim across eight different
// commands (add, current, default, install, list, remove, search,
// vendors) -- every command needs the same "does this tool exist"
// check as its very first step, and had each grown its own identical
// copy over the course of separate rounds of work.
//
// Explicitly printed here, not left to silently return an unprinted
// error -- root.go sets SilenceErrors, so without this the user would
// see nothing but a bare exit code for a simple typo (a real gap
// found via actual testing on `add`, before this was a shared
// function at all).
func requireTool(toolName string) (tooldef.Tool, error) {
	tool, ok := tooldef.Get(toolName)
	if !ok {
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf("\u2717 unknown tool: %s", toolName)))
		return tooldef.Tool{}, fmt.Errorf("unknown tool: %s", toolName)
	}
	return tool, nil
}

// resolveSymlinkPath returns target's real, resolved location if it's
// a symlink, or target unchanged otherwise (including when it's a
// symlink but resolution fails for some reason). Re-checks via the
// filesystem directly rather than trusting a pre-known "is this a
// symlink" flag, so it's correct on its own regardless of the
// caller's context.
func resolveSymlinkPath(target string) string {
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if real, err := os.Readlink(target); err == nil {
			return real
		}
	}
	return target
}

// displayPath returns the path that should be SHOWN to the user for
// a given inventory.Version -- a real bug caught via actual use:
// `list` was showing a `not managed` entry's path as something like
// "~/.sdkkeeper/candidates/java/JDK-25.0.1", which is deeply
// confusing, since "not managed" specifically means it ISN'T one of
// sk's own installs.
//
// v.Path deliberately stores the SYMLINK's own location for external
// (add-registered) entries, not its resolved target -- see
// inventory.Version's own doc comment for why that's the right choice
// for ACTIVATION (the OS follows a symlink transparently when it's
// actually used, e.g. as JAVA_HOME). But that's exactly wrong for
// DISPLAY: a user registered an external JDK at some real location
// and wants to see THAT location, not sk's own internal bookkeeping
// path.
func displayPath(v inventory.Version) string {
	if !v.External {
		return v.Path
	}
	return resolveSymlinkPath(v.Path)
}

// indentLines prepends indent to every non-empty line of s, dropping
// any trailing blank lines -- used after renderTable to apply this
// project's own leading-indent convention (renderTable itself has no
// concept of indentation; it only aligns columns).
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

// renderTable renders rows as an aligned, borderless table -- shared
// by list.go and search.go, both of which previously computed column
// widths and padded manually (fmt.Sprintf("%-*s", ...)). styleFunc
// may be nil (no per-cell styling); rows[i][j] must be PLAIN, never
// pre-styled/ANSI-colored text.
//
// Real, concrete bugs found and fixed while building this, verified
// directly against lipgloss/table's actual behavior (not assumed):
//
//  1. Passing pre-styled (ANSI-colored) cell content breaks table's
//     own width calculation -- confirmed directly: it silently
//     truncated "21.0.2-temurin" to "21.0.2-temuri…" when a
//     neighboring cell carried embedded ANSI codes. This is exactly
//     why styling must go through styleFunc, a plain lipgloss.Style
//     applied to plain text, never baked into the string beforehand.
//  2. table's StyleFunc row index is offset by 1 from the data --
//     row 0 is always reserved for a header row internally, even
//     when none is set via .Headers(). Handled here once, so every
//     caller's own styleFunc can index rows directly by data
//     position, not worry about the header-row offset itself.
//  3. Without an explicit Width(), table auto-detects terminal
//     width -- and that detection fails badly for a non-TTY context
//     (piped output, output redirected to a file), which this CLI's
//     own commands must support (`sk list java > out.txt` is a
//     legitimate, real usage pattern). Confirmed directly: with no
//     width set, running outside a real terminal collapsed every
//     column to near-zero width, truncating everything.
//  4. Disabling BorderTop, BorderBottom, AND BorderHeader together
//     silently drops the table's LAST data row from the render
//     entirely -- a genuine bug in this pinned version (v0.11.0),
//     confirmed with a minimal, isolated repro and narrowed down to
//     exactly that three-flag combination (neither flag alone, nor
//     other combinations, reproduce it). Worked around by leaving
//     BorderHeader at its default; see the border-flag block below
//     for the full reasoning.
//
// The width passed to table is deliberately computed from content,
// with a generous safety margin, rather than table's own
// auto-detection or a precisely minimal calculation -- undershooting
// silently corrupts real data (a truncated version number); a
// slightly too-generous width only adds harmless trailing
// whitespace. Verified directly: a dedicated test asserts every
// input cell's full text is still present, uncut, in the rendered
// output, across several realistic and deliberately adversarial
// shapes (very long entries, extreme length imbalance between
// columns, the specific short-entry case that motivated this in the
// first place).
// tableWidth computes the same content-driven, safety-margined width
// renderTable uses internally -- exposed separately so MULTIPLE,
// separate renderTable calls (e.g. one per vendor sub-group in
// list.go) can share ONE consistent width across all of them, rather
// than each independently sizing itself to only its own rows.
//
// A real, live-reported bug this fixes: Temurin's and Liberica's
// version-listing tables were each computing their own width from
// only their own entries -- when one vendor's longest version number
// was a couple of characters shorter than the other's, that vendor's
// path column started at a visibly different horizontal position,
// reading as inconsistent, "wrongly indented" alignment between two
// blocks that are otherwise meant to look like one continuous table.
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
	// BorderHeader deliberately left at its default (true) -- a real,
	// confirmed bug in this pinned lipgloss/table version (v0.11.0):
	// disabling BorderTop, BorderBottom, AND BorderHeader together
	// silently drops the LAST data row from the render entirely.
	// Verified directly with a minimal repro (3 rows in, only 2 out)
	// and isolated to exactly that three-flag combination -- neither
	// flag alone, nor other combinations, reproduce it. Leaving
	// BorderHeader at its default avoids the bug with no visible
	// side effect, since there's no real header text set anyway (row
	// 0 is always an internal, unused header row regardless -- see
	// this function's own row-index-offset handling below).

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

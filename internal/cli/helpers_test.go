package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"sdkkeeper/internal/inventory"
	"sdkkeeper/internal/tooldef"
)

// captureStderr mirrors output_test.go's captureStdout, for
// requireArgs specifically -- which deliberately writes to os.Stderr,
// not session.Out, since session is genuinely nil at the point cobra
// validates Args (see requireArgs's own doc comment for the full,
// real crash this was found via).
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stderr = w

	fn()

	w.Close()
	os.Stderr = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String()
}

// TestRequireArgs_ValidCountPassesThrough confirms the common case:
// a correct arg count produces no output and no error at all.
func TestRequireArgs_ValidCountPassesThrough(t *testing.T) {
	cmd := &cobra.Command{Use: "add <tool> <version> <path>"}
	validator := requireArgs(cobra.ExactArgs(3))

	out := captureStderr(t, func() {
		if err := validator(cmd, []string{"java", "21.0.2", "/some/path"}); err != nil {
			t.Errorf("expected no error for a valid arg count, got: %v", err)
		}
	})
	if out != "" {
		t.Errorf("expected no output for a valid arg count, got: %q", out)
	}
}

// TestRequireArgs_WrongCountPrintsUsage is a regression test for a
// catastrophic bug caught via actual testing: the first version of
// this function used session.Out, which is genuinely still nil at
// the point cobra calls an Args validator (session/styles are only
// initialized in root.go's PersistentPreRunE, which cobra runs AFTER
// Args validation) -- every single command with a wrong arg count
// panicked with a nil pointer dereference, catastrophically worse
// than the silent failure this was meant to fix. Confirms the real
// fix: plain os.Stderr, no session dependency, no panic, and cmd.Use
// correctly included in the message.
func TestRequireArgs_WrongCountPrintsUsage(t *testing.T) {
	cmd := &cobra.Command{Use: "add <tool> <version> <path>"}
	validator := requireArgs(cobra.ExactArgs(3))

	out := captureStderr(t, func() {
		err := validator(cmd, []string{"java"})
		if err == nil {
			t.Error("expected an error for a wrong arg count")
		}
	})
	if !strings.Contains(out, "usage: sk add <tool> <version> <path>") {
		t.Errorf("expected the usage message to include cmd.Use, got: %q", out)
	}
}

func TestResolveSymlinkPath_RealDirectoryUnchanged(t *testing.T) {
	dir := t.TempDir()
	if got := resolveSymlinkPath(dir); got != dir {
		t.Errorf("expected a real directory's path to be returned unchanged, got: %q", got)
	}
}

func TestResolveSymlinkPath_SymlinkResolvesToRealTarget(t *testing.T) {
	realLocation := t.TempDir()
	symlinkParent := t.TempDir()
	link := filepath.Join(symlinkParent, "JDK-25.0.1")
	if err := os.Symlink(realLocation, link); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	if got := resolveSymlinkPath(link); got != realLocation {
		t.Errorf("expected the symlink to resolve to %q, got: %q", realLocation, got)
	}
}

func TestResolveSymlinkPath_BrokenSymlinkFallsBackToItself(t *testing.T) {
	symlinkParent := t.TempDir()
	link := filepath.Join(symlinkParent, "JDK-25.0.1")
	if err := os.Symlink(filepath.Join(symlinkParent, "does-not-exist"), link); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	// os.Readlink itself still succeeds for a dangling symlink (it
	// just reads the link's own recorded target text) -- confirms
	// resolveSymlinkPath returns that recorded target rather than
	// silently falling back to the link's own path.
	got := resolveSymlinkPath(link)
	want := filepath.Join(symlinkParent, "does-not-exist")
	if got != want {
		t.Errorf("expected the dangling symlink's recorded target %q, got: %q", want, got)
	}
}

// TestDisplayPath_ManagedEntryUnchanged confirms a real, sk-installed
// (non-external) entry's path is never touched, regardless of what
// resolveSymlinkPath would do with it -- displayPath must check
// v.External FIRST, not just delegate unconditionally.
func TestDisplayPath_ManagedEntryUnchanged(t *testing.T) {
	dir := t.TempDir()
	v := inventory.Version{Number: "21.0.2-temurin", Path: dir, External: false}
	if got := displayPath(v); got != dir {
		t.Errorf("expected a managed entry's path to be returned unchanged, got: %q", got)
	}
}

// TestDisplayPath_ExternalEntryShowsRealLocation is a regression test
// for the exact reported bug: `list` showed a "not managed" entry's
// path as the sdkkeeper-internal symlink location (e.g.
// ".sdkkeeper/candidates/java/JDK-25.0.1"), which is precisely wrong
// -- "not managed" specifically means it ISN'T one of sk's own
// installs, so it should never show an sk-internal path at all.
func TestDisplayPath_ExternalEntryShowsRealLocation(t *testing.T) {
	realLocation := t.TempDir()
	symlinkParent := t.TempDir()
	link := filepath.Join(symlinkParent, "JDK-25.0.1")
	if err := os.Symlink(realLocation, link); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	v := inventory.Version{Number: "25.0.1", Path: link, External: true}
	got := displayPath(v)
	if got != realLocation {
		t.Errorf("expected the real, external location %q, got: %q", realLocation, got)
	}
	if got == link {
		t.Errorf("expected NOT to show the sk-internal symlink path %q", link)
	}
}

// setTestHome isolates os.UserHomeDir() for the duration of a test --
// used everywhere a test needs a private ~/.sdkkeeper without ever
// touching the real one.
//
// A real, live-reported bug this fixes: every one of these tests
// previously called t.Setenv("HOME", home) directly, alone -- correct
// on Linux and macOS, where os.UserHomeDir() reads HOME, but a
// complete NO-OP on Windows, confirmed directly from Go's own
// os.UserHomeDir source: on windows, it reads USERPROFILE instead,
// never looking at HOME at all. Every test using only t.Setenv("HOME",
// ...) was, on a real Windows CI runner, silently writing to and
// reading from the REAL runner's actual home directory the entire
// time -- explaining the exact reported symptom: state from one test
// (e.g. a stored default of "99.0.0-temurin") leaking into a
// completely unrelated later test that never should have seen it, on
// Windows specifically, never on Linux or macOS, since neither of
// those platforms exercises this code path at all -- which is exactly
// why this went uncaught until a real Windows CI run surfaced it.
func setTestHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

// TestRenderTable_NeverTruncatesContent is a regression test for a
// real bug found while building this: lipgloss/table miscalculates
// column widths when cell content carries embedded ANSI codes,
// silently truncating real content ("21.0.2-temurin" became
// "21.0.2-temuri…" in a real, live reproduction). renderTable's own
// contract is plain, unstyled cell text with styling applied only via
// styleFunc -- this confirms that contract, once honored, produces no
// truncation across a range of realistic and deliberately adversarial
// shapes, including the exact short-entry case (a bare "11") that
// originally motivated this whole feature.
func TestRenderTable_NeverTruncatesContent(t *testing.T) {
	cases := [][][]string{
		{{"21.0.2-temurin", "(current)"}, {"1.8.0_504-b01-temurin", ""}, {"26.0.2.1-liberica", "(default)"}},
		{{"3.9.9", ""}},
		{{"25.0.1", "(/Users/kumar/.sdkkeeper/candidates/java/JDK-25.0.1)"}},
		{{"11", ""}, {"21.0.12.1", "(current)"}},
		{{"a", "b"}, {"cccccccccccccccccccc", "d"}},
	}
	for _, rows := range cases {
		out := renderTable(rows, 2, tableWidth(rows, 2), nil)
		for _, r := range rows {
			for _, cell := range r {
				if cell == "" {
					continue
				}
				if !strings.Contains(out, cell) {
					t.Errorf("cell %q was truncated or missing in output for rows %v:\n%s", cell, rows, out)
				}
			}
		}
	}
}

// TestRenderTable_NeverDropsRows is a regression test for a real,
// genuinely surprising bug found while building this, in this pinned
// lipgloss/table version (v0.11.0): disabling BorderTop, BorderBottom,
// AND BorderHeader together silently drops the table's LAST data row
// from the render entirely. First caught because real `sk list java`
// output was missing two real, installed JDKs -- isolated to exactly
// that three-flag combination via a minimal, deliberate repro before
// concluding it wasn't a mistake in this project's own code.
func TestRenderTable_NeverDropsRows(t *testing.T) {
	for n := 1; n <= 8; n++ {
		var rows [][]string
		for i := 0; i < n; i++ {
			rows = append(rows, []string{"item" + string(rune('a'+i))})
		}
		out := renderTable(rows, 1, tableWidth(rows, 1), nil)
		for _, r := range rows {
			if !strings.Contains(out, r[0]) {
				t.Errorf("with %d rows, %q is missing from output:\n%s", n, r[0], out)
			}
		}
	}
}

// TestRenderTable_StyleFuncIndexesDataRowsDirectly is a regression
// test for a real, confirmed offset in lipgloss/table's own row
// indexing: row 0 is ALWAYS reserved internally for a header row,
// even when none is set via .Headers(), so a naive styleFunc
// indexing directly into the caller's own data would be off by one
// and panic or misstyle. renderTable absorbs that offset once, here,
// so every caller's own styleFunc can index its data by position
// (styleFunc's row 0 == rows[0]), confirmed directly rather than
// just documented.
func TestRenderTable_StyleFuncIndexesDataRowsDirectly(t *testing.T) {
	rows := [][]string{{"a", "tag-a"}, {"b", "tag-b"}, {"c", "tag-c"}}
	var seenRows []int
	styleFunc := func(row, col int) lipgloss.Style {
		if col == 1 {
			seenRows = append(seenRows, row)
			if row < 0 || row >= len(rows) {
				t.Fatalf("styleFunc called with out-of-range row %d for %d data rows", row, len(rows))
			}
			if rows[row][1] != "tag-"+string(rune('a'+row)) {
				t.Errorf("styleFunc row %d does not correspond to the expected data row, got tag %q", row, rows[row][1])
			}
		}
		return lipgloss.NewStyle()
	}
	renderTable(rows, 2, tableWidth(rows, 2), styleFunc)
	// Called MORE than once per row is expected (table measures each
	// cell's rendered width in one internal pass, then renders in a
	// second) -- confirmed directly, not assumed. What actually
	// matters, and IS checked above on every single call, is that the
	// row index always correctly corresponds to the right data row;
	// the exact call count is an internal implementation detail.
	if len(seenRows) < len(rows) {
		t.Errorf("expected styleFunc to be called at least once per data row (%d), got %d calls", len(rows), len(seenRows))
	}
}

func TestRenderTable_NilStyleFuncIsSafe(t *testing.T) {
	rows := [][]string{{"a", "b"}, {"c", "d"}}
	out := renderTable(rows, 2, tableWidth(rows, 2), nil)
	if !strings.Contains(out, "a") || !strings.Contains(out, "d") {
		t.Errorf("expected all content present with a nil styleFunc, got:\n%s", out)
	}
}

func TestIndentLines_PrependsIndentToEveryLine(t *testing.T) {
	got := indentLines("a\nb\nc", "  ")
	want := "  a\n  b\n  c\n"
	if got != want {
		t.Errorf("indentLines(...) = %q, want %q", got, want)
	}
}

func TestIndentLines_DropsTrailingBlankLines(t *testing.T) {
	got := indentLines("a\nb\n\n\n", "  ")
	want := "  a\n  b\n"
	if got != want {
		t.Errorf("indentLines(...) = %q, want %q -- trailing blank lines should be dropped, not indented", got, want)
	}
}

// TestClearDefaultFile_RemovesEmptyDefaultsDir confirms both real
// gaps this closes: the tool's own default file is removed, AND the
// shared defaults/ directory is removed too once it's the last thing
// left in it -- matching the same "don't leave an empty scratch
// directory behind" pattern TempRoot's own cleanup already uses.
func TestClearDefaultFile_RemovesEmptyDefaultsDir(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	tool, ok := tooldef.Get("java")
	if !ok {
		t.Fatal("java not registered")
	}

	defaultsDir := filepath.Dir(tool.DefaultPath())
	if err := os.MkdirAll(defaultsDir, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.WriteFile(tool.DefaultPath(), []byte("21.0.2-temurin"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	if err := clearDefaultFile(tool); err != nil {
		t.Fatalf("clearDefaultFile failed: %v", err)
	}

	if _, err := os.Stat(tool.DefaultPath()); !os.IsNotExist(err) {
		t.Errorf("expected the default file itself to be gone, stat error: %v", err)
	}
	if _, err := os.Stat(defaultsDir); !os.IsNotExist(err) {
		t.Errorf("expected the now-empty defaults/ directory to be removed too, stat error: %v", err)
	}
}

// TestClearDefaultFile_KeepsDirWhenOtherToolsStillHaveDefaults
// confirms the directory is only removed when it's genuinely empty --
// another tool's own default file must survive untouched.
func TestClearDefaultFile_KeepsDirWhenOtherToolsStillHaveDefaults(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	javaTool, ok := tooldef.Get("java")
	if !ok {
		t.Fatal("java not registered")
	}
	mavenTool, ok := tooldef.Get("maven")
	if !ok {
		t.Fatal("maven not registered")
	}

	defaultsDir := filepath.Dir(javaTool.DefaultPath())
	if err := os.MkdirAll(defaultsDir, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.WriteFile(javaTool.DefaultPath(), []byte("21.0.2-temurin"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.WriteFile(mavenTool.DefaultPath(), []byte("3.9.9"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	if err := clearDefaultFile(javaTool); err != nil {
		t.Fatalf("clearDefaultFile failed: %v", err)
	}

	if _, err := os.Stat(defaultsDir); err != nil {
		t.Errorf("expected defaults/ to still exist (maven's own default is still in it), stat error: %v", err)
	}
	if _, err := os.Stat(mavenTool.DefaultPath()); err != nil {
		t.Errorf("expected maven's own default file to be untouched, stat error: %v", err)
	}
}

func TestClearDefaultFile_AlreadyGoneIsNotAnError(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	tool, ok := tooldef.Get("java")
	if !ok {
		t.Fatal("java not registered")
	}
	if err := clearDefaultFile(tool); err != nil {
		t.Errorf("expected no error clearing an already-nonexistent default, got: %v", err)
	}
}

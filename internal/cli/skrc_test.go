package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindSkrc_NotFoundAnywhereUpToHome(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	sub := filepath.Join(home, "a", "b", "c")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	path, found, err := findSkrc(sub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Errorf("expected not found, got path %q", path)
	}
}

// TestFindSkrc_FoundInImmediateDirectory confirms the simplest case:
// a .skrc sitting directly in cwd, no walking required.
func TestFindSkrc_FoundInImmediateDirectory(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	want := filepath.Join(proj, ".skrc")
	if err := os.WriteFile(want, []byte("java=21.0.2-temurin\n"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	got, found, err := findSkrc(proj)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected to find .skrc")
	}
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// TestFindSkrc_WalksUpFromNestedSubdirectory confirms the whole point
// of the feature: a .skrc several levels above cwd is still found,
// matching git-style config discovery.
func TestFindSkrc_WalksUpFromNestedSubdirectory(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	proj := filepath.Join(home, "proj")
	sub := filepath.Join(proj, "src", "main", "java")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	want := filepath.Join(proj, ".skrc")
	if err := os.WriteFile(want, []byte("java=21.0.2-temurin\n"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	got, found, err := findSkrc(sub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected to find .skrc several directories up")
	}
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// TestFindSkrc_StopsAtHomeBoundary is the regression test for the
// explicit design decision to bound the walk-up at $HOME rather than
// the filesystem root: a .skrc placed ABOVE $HOME must never apply to
// a project below it.
func TestFindSkrc_StopsAtHomeBoundary(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	setTestHome(t, home)

	// A stray .skrc ABOVE $HOME -- must never be found.
	if err := os.WriteFile(filepath.Join(root, ".skrc"), []byte("java=99.0.0\n"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	_, found, err := findSkrc(proj)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected the walk-up to stop at $HOME, not find a .skrc above it")
	}
}

// TestFindSkrc_FoundDirectlyAtHome confirms $HOME itself is still
// checked (the boundary is inclusive, not exclusive) -- only the
// walk NEVER GOES PAST it.
func TestFindSkrc_FoundDirectlyAtHome(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, "proj", "sub")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	setTestHome(t, home)

	want := filepath.Join(home, ".skrc")
	if err := os.WriteFile(want, []byte("java=21.0.2-temurin\n"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	got, found, err := findSkrc(proj)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected to find .skrc directly at $HOME")
	}
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// TestFindSkrc_OutsideHomeStillStopsAtFilesystemRoot confirms the
// fallback: when cwd isn't under $HOME at all (e.g. building in
// /tmp/proj with a fake $HOME elsewhere), the walk still terminates
// cleanly at the filesystem root rather than looping forever.
func TestFindSkrc_OutsideHomeStillStopsAtFilesystemRoot(t *testing.T) {
	setTestHome(t, t.TempDir()) // $HOME never appears in the walk below
	outside := t.TempDir()

	path, found, err := findSkrc(outside)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Errorf("expected not found, got %q", path)
	}
}

func TestParseSkrc_ValidEntriesInOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".skrc")
	content := "java=21.0.2-temurin\nmaven=3.9.9\ngradle=8.5\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	entries, err := parseSkrc(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []skrcEntry{
		{Tool: "java", Version: "21.0.2-temurin"},
		{Tool: "maven", Version: "3.9.9"},
		{Tool: "gradle", Version: "8.5"},
	}
	if len(entries) != len(want) {
		t.Fatalf("expected %d entries, got %d: %+v", len(want), len(entries), entries)
	}
	for i, e := range entries {
		if e != want[i] {
			t.Errorf("entry %d: expected %+v, got %+v", i, want[i], e)
		}
	}
}

// TestParseSkrc_BlankLinesAndCommentsSkipped confirms both the empty
// -file placeholder runInitSkrc writes, and ordinary hand-edited
// comments, parse to zero/normal entries rather than errors.
func TestParseSkrc_BlankLinesAndCommentsSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".skrc")
	content := "# a comment\n\njava=21.0.2-temurin\n\n# another comment\nmaven=3.9.9\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	entries, err := parseSkrc(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(entries), entries)
	}
}

func TestParseSkrc_EmptyCommentOnlyFileYieldsNoEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".skrc")
	content := "# No tools were active when this was generated — sk init skrc\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	entries, err := parseSkrc(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d: %+v", len(entries), entries)
	}
}

func TestParseSkrc_MalformedLineIsAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".skrc")
	if err := os.WriteFile(path, []byte("java 21.0.2-temurin\n"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	if _, err := parseSkrc(path); err == nil {
		t.Error("expected an error for a line with no '='")
	}
}

// TestParseSkrc_ReservedVersionTokensRejected confirms §9's rule: a
// project pin must always be an exact, literal, round-trippable
// version, never the "default"/"null" tokens that are only meaningful
// as `sk use <tool> <arg>`'s own argument slot.
func TestParseSkrc_ReservedVersionTokensRejected(t *testing.T) {
	for _, version := range []string{"default", "null"} {
		dir := t.TempDir()
		path := filepath.Join(dir, ".skrc")
		if err := os.WriteFile(path, []byte("java="+version+"\n"), 0o644); err != nil {
			t.Fatalf("setup failed: %v", err)
		}
		if _, err := parseSkrc(path); err == nil {
			t.Errorf("expected %q to be rejected as a version", version)
		}
	}
}

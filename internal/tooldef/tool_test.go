package tooldef

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProbeDarwinBundle_UsesContentsHomeWhenPresent confirms the
// Temurin-shaped case: a real Contents/Home directory is detected and
// used.
func TestProbeDarwinBundle_UsesContentsHomeWhenPresent(t *testing.T) {
	versionDir := t.TempDir()
	bundlePath := filepath.Join(versionDir, "Contents", "Home")
	if err := os.MkdirAll(bundlePath, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	got := probeDarwinBundle(versionDir)
	if got != bundlePath {
		t.Errorf("expected %q (Contents/Home exists), got %q", bundlePath, got)
	}
}

// TestProbeDarwinBundle_FallsBackWhenAbsent is a regression test for
// a real bug caught via actual use on a real Mac: BellSoft's own
// official install documentation confirms Liberica's macOS archives
// do NOT use the Contents/Home bundle structure at all (JAVA_HOME is
// set directly to the extracted directory, across every version
// checked from 8.x through 25.x) -- unlike Temurin, which does. The
// old, hardcoded logic assumed every java install on darwin used the
// bundle structure, computing a JAVA_HOME for Liberica that didn't
// actually contain a real `bin` directory -- the shell then silently
// fell through to whatever OTHER java happened to still be
// resolvable on PATH, with no error or warning. This confirms the
// fallback: when Contents/Home genuinely doesn't exist, the version
// directory itself is returned unchanged.
func TestProbeDarwinBundle_FallsBackWhenAbsent(t *testing.T) {
	versionDir := t.TempDir()
	// Liberica's real shape: bin/java sits directly under versionDir,
	// no Contents/Home wrapper at all.
	if err := os.MkdirAll(filepath.Join(versionDir, "bin"), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	got := probeDarwinBundle(versionDir)
	if got != versionDir {
		t.Errorf("expected %q (no Contents/Home, fall back to versionDir itself), got %q", versionDir, got)
	}
}

// TestProbeDarwinBundle_FallsBackWhenVersionDirDoesNotExistAtAll
// confirms a nonexistent path doesn't panic or error -- os.Stat on a
// path that doesn't exist at all should behave the same as one that
// exists but lacks Contents/Home: fall back cleanly.
func TestProbeDarwinBundle_FallsBackWhenVersionDirDoesNotExistAtAll(t *testing.T) {
	nonexistent := filepath.Join(t.TempDir(), "does-not-exist")
	got := probeDarwinBundle(nonexistent)
	if got != nonexistent {
		t.Errorf("expected fallback to the given path unchanged, got %q", got)
	}
}

// TestProbeDarwinBundle_RejectsAFileNamedContentsHome confirms the
// check specifically requires Contents/Home to be a DIRECTORY, not
// just any filesystem entry with that name.
func TestProbeDarwinBundle_RejectsAFileNamedContentsHome(t *testing.T) {
	versionDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(versionDir, "Contents"), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	// "Home" exists but as a plain FILE, not a directory.
	if err := os.WriteFile(filepath.Join(versionDir, "Contents", "Home"), []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	got := probeDarwinBundle(versionDir)
	if got != versionDir {
		t.Errorf("expected fallback when Contents/Home exists but isn't a directory, got %q", got)
	}
}

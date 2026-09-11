package cli

import (
	"os"
	"path/filepath"
	"testing"

	"sdkkeeper/internal/tooldef"
)

func TestCheckDanglingRegistrations_None(t *testing.T) {
	setTestHome(t, t.TempDir())
	issues := checkDanglingRegistrations()
	if len(issues) != 0 {
		t.Errorf("expected no issues on a clean state, got: %+v", issues)
	}
}

func TestCheckDanglingRegistrations_FindsDangling(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")

	realTarget := filepath.Join(t.TempDir(), "external-jdk")
	if err := os.MkdirAll(realTarget, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.MkdirAll(tool.CandidateRoot(), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	symlinkPath := filepath.Join(tool.CandidateRoot(), "JDK-99.0.0")
	if err := os.Symlink(realTarget, symlinkPath); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	// Now remove the real target -- the symlink becomes dangling.
	if err := os.RemoveAll(realTarget); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	issues := checkDanglingRegistrations()
	if len(issues) != 1 {
		t.Fatalf("expected exactly 1 dangling registration, got %d: %+v", len(issues), issues)
	}
	if issues[0].warning {
		t.Error("a dangling registration should be an error, not a warning")
	}
}

// TestCheckIncompleteInstalls_None is a regression test for a real
// bug caught via actual testing on a real macOS machine: the test
// fixture originally hardcoded filepath.Join(target, "bin") directly,
// but the real check calls tool.BinPath(v.Path), which on darwin
// resolves to target/Contents/Home/bin (java's own real archive
// layout there), not target/bin. The production check was correct
// all along; the test fixture just didn't account for the same
// darwin-specific transform the real code applies -- it happened to
// pass in a Linux sandbox (where BinPath is just versionDir/bin, no
// transform), and only failed once actually run on a real Mac. Fixed
// by building the fixture at tool.BinPath(target), the exact same
// path-computation function the real code uses, so this test is
// correct on any platform rather than accidentally Linux-only.
func TestCheckIncompleteInstalls_None(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")

	target := filepath.Join(tool.CandidateRoot(), "JDK-21.0.2-temurin")
	binPath := tool.BinPath(target)
	if err := os.MkdirAll(binPath, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binPath, "java"), []byte("fake"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	issues := checkIncompleteInstalls()
	if len(issues) != 0 {
		t.Errorf("expected no issues for a structurally complete install, got: %+v", issues)
	}
}

// TestCheckIncompleteInstalls_FindsEmptyBin -- same tool.BinPath(target)
// fix as TestCheckIncompleteInstalls_None above, for the same reason:
// correctness on any platform, not just Linux where BinPath happens
// to equal a hardcoded "bin" subdirectory.
func TestCheckIncompleteInstalls_FindsEmptyBin(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")

	target := filepath.Join(tool.CandidateRoot(), "JDK-21.0.2-temurin")
	if err := os.MkdirAll(tool.BinPath(target), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	// bin exists but is empty -- structurally incomplete.

	issues := checkIncompleteInstalls()
	if len(issues) != 1 {
		t.Fatalf("expected exactly 1 incomplete install, got %d: %+v", len(issues), issues)
	}
}

func TestCheckIncompleteInstalls_IgnoresSymlinks(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")

	// A registered (symlinked) entry with no bin directory at all --
	// this should NOT be flagged as "incomplete", since the structural
	// completeness of an EXTERNAL install isn't sk's install to judge.
	realTarget := t.TempDir()
	if err := os.MkdirAll(tool.CandidateRoot(), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	symlinkPath := filepath.Join(tool.CandidateRoot(), "JDK-17.0.3")
	if err := os.Symlink(realTarget, symlinkPath); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	issues := checkIncompleteInstalls()
	if len(issues) != 0 {
		t.Errorf("expected symlinked entries to never be flagged as incomplete, got: %+v", issues)
	}
}

func TestCheckStaleDefaults_None(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")

	target := filepath.Join(tool.CandidateRoot(), "JDK-21.0.2-temurin")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(tool.DefaultPath()), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.WriteFile(tool.DefaultPath(), []byte("21.0.2-temurin"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	issues := checkStaleDefaults()
	if len(issues) != 0 {
		t.Errorf("expected no issues when the default matches a real, present version, got: %+v", issues)
	}
}

func TestCheckStaleDefaults_FindsStale(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")

	if err := os.MkdirAll(filepath.Dir(tool.DefaultPath()), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.WriteFile(tool.DefaultPath(), []byte("99.0.0-temurin"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	// Deliberately nothing installed at all -- the stored default
	// can't possibly correspond to anything real.

	issues := checkStaleDefaults()
	if len(issues) != 1 {
		t.Fatalf("expected exactly 1 stale default, got %d: %+v", len(issues), issues)
	}
}

func TestCheckLeftoverTempDirs_None(t *testing.T) {
	setTestHome(t, t.TempDir())
	issues := checkLeftoverTempDirs()
	if len(issues) != 0 {
		t.Errorf("expected no issues when TempRoot doesn't even exist, got: %+v", issues)
	}
}

func TestCheckLeftoverTempDirs_FindsLeftovers(t *testing.T) {
	setTestHome(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(tooldef.TempRoot(), "extract-12345"), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	issues := checkLeftoverTempDirs()
	if len(issues) != 1 {
		t.Fatalf("expected exactly 1 leftover temp dir, got %d: %+v", len(issues), issues)
	}
	if !issues[0].warning {
		t.Error("a leftover temp directory should be a warning, not an error -- harmless clutter, not a functional problem")
	}
}

func TestSortedTools_StableOrder(t *testing.T) {
	first := sortedTools()
	for i := 0; i < 10; i++ {
		got := sortedTools()
		if len(got) != len(first) {
			t.Fatalf("expected consistent length, got %d vs %d", len(got), len(first))
		}
		for j := range first {
			if got[j].Name != first[j].Name {
				t.Errorf("expected stable order across calls, got %v vs %v", got, first)
			}
		}
	}
}

package cli

import (
	"os"
	"path/filepath"
	"testing"

	"sdkkeeper/internal/inventory"
)

// TestRemoveVersion_RealDirectoryDeletesFiles confirms a genuine,
// sk-installed directory (External=false) is actually deleted --
// os.RemoveAll, the real files, since sk itself put them there.
func TestRemoveVersion_RealDirectoryDeletesFiles(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "JDK-21.0.2-temurin")
	if err := os.MkdirAll(filepath.Join(target, "bin"), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "bin", "java"), []byte("fake"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	v := inventory.Version{Number: "21.0.2-temurin", Path: target, External: false}
	if err := removeVersion(v); err != nil {
		t.Fatalf("removeVersion failed: %v", err)
	}

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("expected the real directory to be fully deleted, got Stat error: %v", err)
	}
}

// TestRemoveVersion_SymlinkOnlyRemovesTheLinkNotTheRealFiles is the
// single most important test in this package: confirms that removing
// a REGISTERED (symlinked) entry only deletes the symlink itself,
// NEVER the real files it points at -- those belong to the user, not
// sk, and getting this backwards (e.g. accidentally using RemoveAll on
// a symlink, which would follow it and delete the user's actual
// external JDK) would be a genuinely serious, hard-to-undo bug.
func TestRemoveVersion_SymlinkOnlyRemovesTheLinkNotTheRealFiles(t *testing.T) {
	realExternalDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(realExternalDir, "marker.txt"), []byte("real user file"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	symlinkParent := t.TempDir()
	symlinkPath := filepath.Join(symlinkParent, "JDK-17.0.3")
	if err := os.Symlink(realExternalDir, symlinkPath); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	v := inventory.Version{Number: "17.0.3", Path: symlinkPath, External: true}
	if err := removeVersion(v); err != nil {
		t.Fatalf("removeVersion failed: %v", err)
	}

	// The symlink itself must be gone.
	if _, err := os.Lstat(symlinkPath); !os.IsNotExist(err) {
		t.Errorf("expected the symlink itself to be removed, got Lstat error: %v", err)
	}

	// The REAL external directory and its contents must be COMPLETELY
	// untouched -- this is the critical assertion.
	content, err := os.ReadFile(filepath.Join(realExternalDir, "marker.txt"))
	if err != nil {
		t.Fatalf("CRITICAL: real external file was affected by removing the registration: %v", err)
	}
	if string(content) != "real user file" {
		t.Errorf("real external file content changed unexpectedly: %q", content)
	}
	if _, err := os.Stat(realExternalDir); err != nil {
		t.Fatalf("CRITICAL: real external directory itself was affected: %v", err)
	}
}

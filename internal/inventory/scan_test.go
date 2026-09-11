package inventory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sdkkeeper/internal/tooldef"
)

func TestScan_CandidateRootOnly(t *testing.T) {
	tmp := t.TempDir()
	mustMkdirVersion(t, tmp, "JDK-21.0.2")
	mustMkdirVersion(t, tmp, "JDK-17.0.3")
	mustMkdirVersion(t, tmp, "JDK-8.0")

	tool := tooldef.Tool{
		Name:         "java",
		FolderPrefix: "JDK-",
	}
	found, err := scanRoot(tmp, tool.FolderPrefix)
	if err != nil {
		t.Fatalf("scanRoot failed: %v", err)
	}
	if len(found) != 3 {
		t.Fatalf("expected 3 versions, got %d: %+v", len(found), found)
	}
	for _, v := range found {
		if v.External {
			t.Errorf("expected a real directory to be non-external, got: %+v", v)
		}
	}
}

// TestScan_SymlinkIsExternal is the replacement for the old
// TestScan_MergesLegacyRoots, now that hardcoded LegacyRoots have been
// removed entirely in favor of `add`-created symlinks (see design doc
// §10.6 and the removal of Tool.LegacyRoots). A symlinked entry --
// regardless of where it actually points -- should be scanned
// correctly (a real bug existed here: os.DirEntry.IsDir() is false for
// a symlink even when it points at a directory, so the entry-filtering
// logic must explicitly allow symlinks too, not just real directories)
// and tagged External.
func TestScan_SymlinkIsExternal(t *testing.T) {
	root := t.TempDir()
	realTarget := t.TempDir() // simulates wherever the actual install lives

	mustMkdirVersion(t, root, "JDK-21.0.2") // a real, `install`-style entry
	if err := os.Symlink(realTarget, filepath.Join(root, "JDK-17.0.3")); err != nil {
		t.Fatalf("failed to create test symlink: %v", err)
	}

	found, err := scanRoot(root, "JDK-")
	if err != nil {
		t.Fatalf("scanRoot failed: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("expected 2 versions (one real, one symlinked), got %d: %+v", len(found), found)
	}

	var sawExternal, sawManaged bool
	for _, v := range found {
		if v.External {
			sawExternal = true
			if v.Number != "17.0.3" {
				t.Errorf("expected the symlinked entry to be 17.0.3, got: %+v", v)
			}
		} else {
			sawManaged = true
			if v.Number != "21.0.2" {
				t.Errorf("expected the real-directory entry to be 21.0.2, got: %+v", v)
			}
		}
	}
	if !sawExternal || !sawManaged {
		t.Errorf("expected one external (symlink) and one managed (real dir) entry, got: %+v", found)
	}
}

// TestScan_DanglingSymlinkStillListed confirms a symlink pointing at a
// now-deleted target is still scanned and listed (design doc §10.6:
// detecting/reporting brokenness is `sk doctor`'s job, not scan's --
// scan should never silently hide a real, registered entry just
// because its target has gone away).
func TestScan_DanglingSymlinkStillListed(t *testing.T) {
	root := t.TempDir()
	deletedTarget := filepath.Join(t.TempDir(), "no-longer-here")

	if err := os.Symlink(deletedTarget, filepath.Join(root, "JDK-21.0.2")); err != nil {
		t.Fatalf("failed to create test symlink: %v", err)
	}

	found, err := scanRoot(root, "JDK-")
	if err != nil {
		t.Fatalf("scanRoot failed: %v", err)
	}
	if len(found) != 1 || !found[0].External {
		t.Fatalf("expected the dangling symlink to still be listed as external, got: %+v", found)
	}
}

func TestScan_MissingRootIsNotAnError(t *testing.T) {
	_, err := scanRoot("/definitely/does/not/exist/anywhere", "JDK-")
	if err == nil {
		t.Fatal("expected an error for a missing root")
	}
	if !os.IsNotExist(err) {
		t.Errorf("expected os.IsNotExist(err) to be true, got: %v", err)
	}
}

func TestFind(t *testing.T) {
	tmp := t.TempDir()
	mustMkdirVersion(t, tmp, "JDK-21.0.2")

	found, err := scanRoot(tmp, "JDK-")
	if err != nil {
		t.Fatalf("scanRoot failed: %v", err)
	}
	if len(found) != 1 || found[0].Number != "21.0.2" {
		t.Fatalf("unexpected scan result: %+v", found)
	}
}

func mustMkdirVersion(t *testing.T, root, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
		t.Fatalf("failed to create test dir: %v", err)
	}
}

// TestFormatNotFound_NonEmptyVersionsListsThem confirms the normal
// case, unaffected by the empty-versions fix below: when versions
// exist, they're listed under "Available versions:", as before.
func TestFormatNotFound_NonEmptyVersionsListsThem(t *testing.T) {
	versions := []Version{{Number: "21.0.2-temurin"}, {Number: "17.0.3-temurin"}}
	msg := FormatNotFound("JDK-", "99.99-temurin", "use", "JDK", versions)

	if !strings.Contains(msg, "JDK-99.99-temurin not found — nothing to use") {
		t.Errorf("expected the not-found header with the action, got: %q", msg)
	}
	if !strings.Contains(msg, "Available versions:") {
		t.Errorf("expected an 'Available versions:' header when versions exist, got: %q", msg)
	}
	if !strings.Contains(msg, "21.0.2-temurin") || !strings.Contains(msg, "17.0.3-temurin") {
		t.Errorf("expected both available versions listed, got: %q", msg)
	}
}

// TestFormatNotFound_EmptyVersionsShowsClearMessage is a regression
// test for a real gap found via actual use: with NOTHING installed
// or added at all, this used to print an "Available versions:"
// header followed by nothing underneath it at all -- reading as a
// possible display bug rather than a clear, honest "there is
// nothing" statement. Confirms the fix reuses the EXACT phrasing
// `list` already established for this identical "nothing installed
// or added yet" situation, rather than a differently-worded message
// for what is, from the user's perspective, the same state.
func TestFormatNotFound_EmptyVersionsShowsClearMessage(t *testing.T) {
	msg := FormatNotFound("JDK-", "25.0.1", "use", "JDK", nil)

	if !strings.Contains(msg, "JDK-25.0.1 not found — nothing to use") {
		t.Errorf("expected the not-found header with the action, got: %q", msg)
	}
	if strings.Contains(msg, "Available versions:") {
		t.Errorf("expected NO empty 'Available versions:' header when nothing is installed, got: %q", msg)
	}
	if !strings.Contains(msg, "No JDK versions installed or added yet.") {
		t.Errorf("expected the same clear message 'list' uses for this identical state, got: %q", msg)
	}
}

// TestFormatNotFound_EmptyVersionsUsesCorrectDisplayName confirms the
// displayName parameter is actually used, not hardcoded -- e.g. for
// Maven, not always "JDK".
func TestFormatNotFound_EmptyVersionsUsesCorrectDisplayName(t *testing.T) {
	msg := FormatNotFound("apache-maven-", "3.9.14", "remove", "Maven", nil)
	if !strings.Contains(msg, "No Maven versions installed or added yet.") {
		t.Errorf("expected the Maven-specific display name, got: %q", msg)
	}
}

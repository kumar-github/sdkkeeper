package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sdkkeeper/internal/inventory"
	"sdkkeeper/internal/tooldef"
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

func TestResolveRemoveJSON_UnknownToolIsAmbiguousTool(t *testing.T) {
	setTestHome(t, t.TempDir())
	_, jerr := resolveRemoveJSON("not-a-real-tool", "1.0")
	if jerr == nil || jerr.Code != ErrCodeAmbiguousTool {
		t.Fatalf("expected ambiguous_tool, got %+v", jerr)
	}
}

// TestResolveRemoveJSON_NoVersionIsVersionRequired confirms design doc
// §6 applies to `remove` too -- no picker under --format=json, ever.
func TestResolveRemoveJSON_NoVersionIsVersionRequired(t *testing.T) {
	setTestHome(t, t.TempDir())
	_, jerr := resolveRemoveJSON("java", "")
	if jerr == nil || jerr.Code != ErrCodeVersionRequired {
		t.Fatalf("expected version_required, got %+v", jerr)
	}
}

func TestResolveRemoveJSON_UnknownVersionIsNotFound(t *testing.T) {
	setTestHome(t, t.TempDir())
	_, jerr := resolveRemoveJSON("java", "99.0.0-temurin")
	if jerr == nil || jerr.Code != ErrCodeNotFound {
		t.Fatalf("expected not_found, got %+v", jerr)
	}
}

// TestResolveRemoveJSON_RealVersionIsActuallyDeletedFromDisk is the
// main happy path: confirms the JSON path performs the SAME real
// deletion the interactive path does (not just reporting success
// without acting), and reports the correct vendor + "removed" action.
func TestResolveRemoveJSON_RealVersionIsActuallyDeletedFromDisk(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-liberica")
	tool, _ := tooldef.Get("java")
	dir := filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+"21.0.2-liberica")

	data, jerr := resolveRemoveJSON("java", "21.0.2-liberica")
	if jerr != nil {
		t.Fatalf("expected success, got error: %+v", jerr)
	}
	if data.Action != string(actionRemoved) {
		t.Errorf("expected action=removed, got %q", data.Action)
	}
	if data.Vendor == nil || *data.Vendor != "liberica" {
		t.Errorf("expected vendor=liberica, got %v", data.Vendor)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("expected the real directory to be deleted from disk, got Stat error: %v", err)
	}
}

// TestResolveRemoveJSON_ClearsMatchingStoredDefault mirrors the
// interactive path's own "removing the current default clears it too"
// behavior (see remove.go's clearedDefault logic) -- a real, confirmed
// gap that would otherwise leave `sk default java` (or its own
// --format=json counterpart) pointing at a version that no longer
// exists.
func TestResolveRemoveJSON_ClearsMatchingStoredDefault(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")
	tool, _ := tooldef.Get("java")
	if err := os.MkdirAll(filepath.Dir(tool.DefaultPath()), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.WriteFile(tool.DefaultPath(), []byte("21.0.2-temurin"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	if _, jerr := resolveRemoveJSON("java", "21.0.2-temurin"); jerr != nil {
		t.Fatalf("expected success, got error: %+v", jerr)
	}

	if _, err := os.Stat(tool.DefaultPath()); !os.IsNotExist(err) {
		t.Errorf("expected the now-dangling default file to be cleared, got Stat error: %v", err)
	}
}

// TestResolveRemoveJSON_ReportsWasCurrentAndWasDefault is the direct
// regression test for a real, reported bug: removing the version that
// is BOTH the currently-active one (via the tool's real env var) AND
// the stored default previously reported neither fact in the JSON
// payload at all -- a caller (a script, an agent, a human reading the
// output) had no way to learn its OWN shell's JAVA_HOME was now
// dangling, since --format=json can't unset it unprompted (same
// inherent limitation as resolveUseJSON's own "does not mutate the
// shell" behavior). WasCurrent/WasDefault exist specifically to make
// that visible.
func TestResolveRemoveJSON_ReportsWasCurrentAndWasDefault(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")
	tool, _ := tooldef.Get("java")
	dir := filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+"21.0.2-temurin")

	if err := os.MkdirAll(filepath.Dir(tool.DefaultPath()), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.WriteFile(tool.DefaultPath(), []byte("21.0.2-temurin"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	t.Setenv(tool.EnvVar, tool.HomePath(dir))

	data, jerr := resolveRemoveJSON("java", "21.0.2-temurin")
	if jerr != nil {
		t.Fatalf("expected success, got error: %+v", jerr)
	}
	if !data.WasCurrent {
		t.Error("expected WasCurrent=true -- this version's env var matched exactly")
	}
	if !data.WasDefault {
		t.Error("expected WasDefault=true -- this version was the stored default")
	}
	if data.EnvVar != "JAVA_HOME" {
		t.Errorf("expected envVar=JAVA_HOME so a caller knows exactly what to unset, got %q", data.EnvVar)
	}
	if want := tool.BinPath(dir); data.BinPath != want {
		t.Errorf("expected binPath=%q (the exact PATH entry a caller would need to strip), got %q", want, data.BinPath)
	}
}

// TestResolveRemoveJSON_ReportsFalseWhenNeitherCurrentNorDefault
// confirms the negative case isn't just a hardcoded true -- removing
// an installed-but-inactive, non-default version reports both flags
// false.
func TestResolveRemoveJSON_ReportsFalseWhenNeitherCurrentNorDefault(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")
	installFakeJava(t, home, "17.0.9-temurin")
	tool, _ := tooldef.Get("java")
	activeDir := filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+"17.0.9-temurin")
	t.Setenv(tool.EnvVar, tool.HomePath(activeDir)) // a DIFFERENT version is active

	data, jerr := resolveRemoveJSON("java", "21.0.2-temurin")
	if jerr != nil {
		t.Fatalf("expected success, got error: %+v", jerr)
	}
	if data.WasCurrent {
		t.Error("expected WasCurrent=false -- a different version was active")
	}
	if data.WasDefault {
		t.Error("expected WasDefault=false -- no default was ever set")
	}
	// EnvVar/BinPath are reported UNCONDITIONALLY -- same "always
	// present, caller decides whether to act on it" pattern as
	// Vendor -- not only when WasCurrent happens to be true.
	if data.EnvVar != "JAVA_HOME" {
		t.Errorf("expected envVar to still be reported even when WasCurrent is false, got %q", data.EnvVar)
	}
	removedDir := filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+"21.0.2-temurin")
	if want := tool.BinPath(removedDir); data.BinPath != want {
		t.Errorf("expected binPath=%q for the version actually removed, got %q", want, data.BinPath)
	}
}

// TestRemove_NoVersionWithoutTTYReturnsVersionRequired is remove's own
// counterpart to resolve_test.go's identical test for `use` -- design
// doc §6's "harden the existing automatic TTY-detection fallback".
// Invokes the actual cobra command (via withSession, which sets the
// package-level session to a zero-HasTTY Session, exactly like every
// other test in this package that never opens a real terminal) rather
// than a standalone function, since remove's no-version/picker logic
// lives directly in its RunE closure, unlike use's (which delegates
// to the separately-testable resolveUse).
func TestRemove_NoVersionWithoutTTYReturnsVersionRequired(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")

	var runErr error
	out := withSession(t, func() {
		cmd := newRemoveCmd()
		runErr = cmd.RunE(cmd, []string{"java"})
	})

	var cliErr *CLIError
	if !errors.As(runErr, &cliErr) {
		t.Fatalf("expected a *CLIError, got: %T (%v)", runErr, runErr)
	}
	if cliErr.Code != ErrCodeVersionRequired {
		t.Errorf("expected version_required, got %q", cliErr.Code)
	}
	if ExitCode(runErr) != 101 {
		t.Errorf("expected exit code 101, got %d", ExitCode(runErr))
	}
	if strings.Contains(out, "/dev/tty") {
		t.Errorf("expected the picker's own low-level wording to NEVER reach the user, got: %q", out)
	}
	if !strings.Contains(out, "a version is required") {
		t.Errorf("expected a clear, actionable message, got: %q", out)
	}
}

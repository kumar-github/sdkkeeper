package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sdkkeeper/internal/inventory"
	"sdkkeeper/internal/tooldef"
)

func writeFakeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
}

func TestDirSize_SumsAllFilesRecursively(t *testing.T) {
	dir := t.TempDir()
	writeFakeFile(t, filepath.Join(dir, "a.txt"), 100)
	writeFakeFile(t, filepath.Join(dir, "sub", "b.txt"), 250)
	writeFakeFile(t, filepath.Join(dir, "sub", "deeper", "c.txt"), 7)

	got := dirSize(dir)
	want := int64(100 + 250 + 7)
	if got != want {
		t.Errorf("expected %d, got %d", want, got)
	}
}

func TestDirSize_EmptyDirIsZero(t *testing.T) {
	dir := t.TempDir()
	if got := dirSize(dir); got != 0 {
		t.Errorf("expected 0, got %d", got)
	}
}

func TestDirSize_NonexistentPathIsZeroNotError(t *testing.T) {
	// Best-effort: dirSize has no error return at all -- a path that
	// can't be walked (doesn't exist, permission denied) just
	// contributes 0, it never panics or aborts the caller.
	if got := dirSize(filepath.Join(t.TempDir(), "does-not-exist")); got != 0 {
		t.Errorf("expected 0 for a nonexistent path, got %d", got)
	}
}

func TestBuildListJSON_SizesOmittedByDefault(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")

	data, jerr := buildListJSON("java", false)
	if jerr != nil {
		t.Fatalf("expected success, got: %+v", jerr)
	}
	if len(data.Installed) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(data.Installed))
	}
	if data.Installed[0].SizeBytes != nil {
		t.Errorf("expected SizeBytes to be nil (omitted) when sizes not requested, got %v", *data.Installed[0].SizeBytes)
	}
}

func TestBuildListJSON_SizesPopulatedWhenRequested(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")
	dir := filepath.Join(tool.CandidateRoot(), "JDK-21.0.2-temurin")
	writeFakeFile(t, filepath.Join(dir, "bin", "java"), 1000)
	writeFakeFile(t, filepath.Join(dir, "lib", "modules"), 2000)

	data, jerr := buildListJSON("java", true)
	if jerr != nil {
		t.Fatalf("expected success, got: %+v", jerr)
	}
	if len(data.Installed) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(data.Installed))
	}
	if data.Installed[0].SizeBytes == nil {
		t.Fatal("expected SizeBytes to be populated when sizes requested")
	}
	if *data.Installed[0].SizeBytes != 3000 {
		t.Errorf("expected 3000, got %d", *data.Installed[0].SizeBytes)
	}
}

func TestPrintToolListBody_SizesShownOnlyWhenRequested(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")
	dir := filepath.Join(tool.CandidateRoot(), "JDK-21.0.2-temurin")
	writeFakeFile(t, filepath.Join(dir, "bin", "java"), 5*1024*1024) // 5 MB

	versionsNoSizes := captureStdout(t, func() {
		versions := mustScan(t, tool)
		printToolListBody(tool, versions, false)
	})
	if strings.Contains(versionsNoSizes, "MB") {
		t.Errorf("expected no size info without --sizes, got: %q", versionsNoSizes)
	}

	out := captureStdout(t, func() {
		versions := mustScan(t, tool)
		total := printToolListBody(tool, versions, true)
		if total != 5*1024*1024 {
			t.Errorf("expected returned total 5MB, got %d", total)
		}
	})
	if !strings.Contains(out, "MB") {
		t.Errorf("expected size info with --sizes, got: %q", out)
	}
	if !strings.Contains(out, "JDK total:") {
		t.Errorf("expected a per-tool subtotal line, got: %q", out)
	}
}

func TestPrintAllToolsList_GrandTotalOnlyShownWithSizes(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")
	writeFakeFile(t, filepath.Join(tool.CandidateRoot(), "JDK-21.0.2-temurin", "bin", "java"), 1024*1024)

	withoutSizes := captureStdout(t, func() {
		if err := printAllToolsList(false); err != nil {
			t.Fatalf("expected success, got: %v", err)
		}
	})
	if strings.Contains(withoutSizes, "Grand total") {
		t.Errorf("expected no grand total without --sizes, got: %q", withoutSizes)
	}

	withSizes := captureStdout(t, func() {
		if err := printAllToolsList(true); err != nil {
			t.Fatalf("expected success, got: %v", err)
		}
	})
	if !strings.Contains(withSizes, "Grand total: 1.0 MB") {
		t.Errorf("expected the exact grand total line, got: %q", withSizes)
	}
}

func TestListCmd_SizesFlagWiredThroughToJSON(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")
	writeFakeFile(t, filepath.Join(tool.CandidateRoot(), "JDK-21.0.2-temurin", "bin", "java"), 42)

	oldFormat := outputFormat
	outputFormat = FormatJSON
	defer func() { outputFormat = oldFormat }()

	cmd := newListCmd()
	if err := cmd.Flags().Set("sizes", "true"); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	var runErr error
	out := captureStdout(t, func() {
		runErr = cmd.RunE(cmd, []string{"java"})
	})
	if runErr != nil {
		t.Fatalf("expected success, got: %v", runErr)
	}
	if !strings.Contains(out, `"sizeBytes":42`) {
		t.Errorf("expected sizeBytes:42 in the JSON output, got: %q", out)
	}
}

// mustScan is a small test helper -- inventory.Scan wrapped with a
// t.Fatalf on error, since every caller in this file wants that.
func mustScan(t *testing.T, tool tooldef.Tool) []inventory.Version {
	t.Helper()
	versions, err := inventory.Scan(tool)
	if err != nil {
		t.Fatalf("inventory.Scan failed: %v", err)
	}
	return versions
}

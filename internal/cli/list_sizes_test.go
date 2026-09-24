package cli

import (
	"fmt"
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

// TestDirSize_FollowsSymlinkRoot is the regression test for the real
// bug found in this session: filepath.WalkDir does NOT follow a
// symlink that is itself the root it's given -- it reports that root
// as a single non-directory leaf and returns the symlink's OWN lstat
// size (the byte-length of the target path string), never descending
// into what it actually points to. Every `add`-registered entry's
// v.Path IS such a root symlink, so without resolving it first, every
// external entry's size silently collapsed to a tiny, meaningless
// number (confirmed directly: a real 50 MB directory measured as 25
// bytes via its symlink, before this fix).
func TestDirSize_FollowsSymlinkRoot(t *testing.T) {
	realDir := filepath.Join(t.TempDir(), "real-jdk")
	writeFakeFile(t, filepath.Join(realDir, "bin", "java"), 5*1024*1024)
	symlinkPath := filepath.Join(t.TempDir(), "symlink-to-jdk")
	if err := os.Symlink(realDir, symlinkPath); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	realSize := dirSize(realDir)
	viaSymlink := dirSize(symlinkPath)
	if realSize != 5*1024*1024 {
		t.Fatalf("expected the real dir itself to measure 5 MB, got %d -- test setup is broken", realSize)
	}
	if viaSymlink != realSize {
		t.Errorf("expected walking via the symlink to match the real directory's size (%d), got %d", realSize, viaSymlink)
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
		printToolListBody(tool, versions, nil, 0, 0, false)
	})
	if strings.Contains(versionsNoSizes, "MB") {
		t.Errorf("expected no size info without --sizes, got: %q", versionsNoSizes)
	}

	out := captureStdout(t, func() {
		versions := mustScan(t, tool)
		sizes, total := computeSizes(versions)
		if total != 5*1024*1024 {
			t.Errorf("expected computed total 5MB, got %d", total)
		}
		printToolListBody(tool, versions, sizes, total, 0, true)
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

// registerExternal creates a real symlink at
// <tool candidates>/<FolderPrefix><version>, pointing at a real
// directory containing a file of the given size -- the same shape
// `sk add` itself creates, so inventory.Scan reports it as a genuine
// External entry, not a hand-built struct literal.
func registerExternal(t *testing.T, tool tooldef.Tool, version string, fileSize int) string {
	t.Helper()
	realDir := filepath.Join(t.TempDir(), "external-"+version)
	writeFakeFile(t, filepath.Join(realDir, "bin", "tool"), fileSize)
	if err := os.MkdirAll(tool.CandidateRoot(), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	symlinkPath := filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+version)
	if err := os.Symlink(realDir, symlinkPath); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	return realDir
}

// TestBuildListJSON_ExternalEntryReportsRealSizeAndManagedFalse is the
// integration-level regression test: a real `add`-registered entry,
// through the actual buildListJSON path (not a hand-built
// inventory.Version), must report its REAL target size (not the
// symlink's own tiny lstat size) and Managed:false.
func TestBuildListJSON_ExternalEntryReportsRealSizeAndManagedFalse(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")
	registerExternal(t, tool, "99.0.0-external", 3*1024*1024)

	data, jerr := buildListJSON("java", true)
	if jerr != nil {
		t.Fatalf("expected success, got: %+v", jerr)
	}
	if len(data.Installed) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(data.Installed))
	}
	entry := data.Installed[0]
	if entry.Managed {
		t.Error("expected Managed=false for an add-registered entry")
	}
	if entry.SizeBytes == nil || *entry.SizeBytes != 3*1024*1024 {
		got := "nil"
		if entry.SizeBytes != nil {
			got = fmt.Sprintf("%d", *entry.SizeBytes)
		}
		t.Errorf("expected SizeBytes=3145728 (the REAL target size), got %s", got)
	}
}

func TestBuildListJSON_ManagedTrueForRealInstall(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")

	data, jerr := buildListJSON("java", false)
	if jerr != nil {
		t.Fatalf("expected success, got: %+v", jerr)
	}
	if len(data.Installed) != 1 || !data.Installed[0].Managed {
		t.Errorf("expected Managed=true for a real sk install, got: %+v", data.Installed)
	}
}

// TestPrintToolListBody_ExternalEntryGetsTipAndCountsTowardTotal
// confirms the text-output side of the same fix: the real size is
// used (not the symlink's tiny lstat size), it's included in the
// tool's total (per the user's own explicit decision), and the
// "not managed" tip appears -- but ONLY when there's actually an
// external entry to warn about.
func TestPrintToolListBody_ExternalEntryGetsTipAndCountsTowardTotal(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")
	installFakeJava(t, home, "21.0.2-temurin")
	writeFakeFile(t, filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+"21.0.2-temurin", "bin", "java"), 2*1024*1024)
	registerExternal(t, tool, "99.0.0-external", 3*1024*1024)

	var total int64
	out := captureStdout(t, func() {
		versions := mustScan(t, tool)
		var sizes map[string]int64
		sizes, total = computeSizes(versions)
		printToolListBody(tool, versions, sizes, total, 0, true)
	})
	if total != 5*1024*1024 {
		t.Errorf("expected the total to include BOTH the managed (2MB) and external (3MB) entries = 5MB, got %d", total)
	}
	if !strings.Contains(out, "not managed by SDK Keeper -- `sk remove` won't free") {
		t.Errorf("expected the not-managed tip, got: %q", out)
	}
}

// TestPrintToolListBody_NoTipWhenNothingExternal confirms the tip is
// NOT shown for a tool with only managed installs -- it would be
// noise, not a warning, in that case.
func TestPrintToolListBody_NoTipWhenNothingExternal(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")
	installFakeJava(t, home, "21.0.2-temurin")
	writeFakeFile(t, filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+"21.0.2-temurin", "bin", "java"), 1024)

	out := captureStdout(t, func() {
		versions := mustScan(t, tool)
		sizes, total := computeSizes(versions)
		printToolListBody(tool, versions, sizes, total, 0, true)
	})
	if strings.Contains(out, "not managed by SDK Keeper") {
		t.Errorf("expected no tip when nothing is external, got: %q", out)
	}
}

func TestSizeBar_ProportionalFill(t *testing.T) {
	cases := []struct {
		size, maxSize int64
		wantFilled    int
	}{
		{size: 0, maxSize: 100, wantFilled: 0},
		{size: 100, maxSize: 100, wantFilled: 10}, // the max itself -- always fully filled
		{size: 50, maxSize: 100, wantFilled: 5},
		{size: 10, maxSize: 100, wantFilled: 1},
	}
	for _, c := range cases {
		bar := sizeBar(c.size, c.maxSize)
		got := strings.Count(bar, "\u2588")
		if got != c.wantFilled {
			t.Errorf("sizeBar(%d, %d): expected %d filled chars, got %d (%q)", c.size, c.maxSize, c.wantFilled, got, bar)
		}
		if len([]rune(bar)) != 10 {
			t.Errorf("sizeBar(%d, %d): expected a fixed 10-char bar, got %d chars (%q)", c.size, c.maxSize, len([]rune(bar)), bar)
		}
	}
}

func TestSizeBar_ZeroMaxSizeIsAllEmpty(t *testing.T) {
	bar := sizeBar(0, 0)
	if strings.Count(bar, "\u2588") != 0 {
		t.Errorf("expected an all-empty bar when maxSize is 0, got %q", bar)
	}
}

// TestPrintToolListBody_SortsBySizeDescendingWhenRequested confirms
// improvement #3: reaching for --sizes at all means "what's eating my
// disk", so the biggest entry belongs first -- not wherever it falls
// in the normal newest-first version order.
func TestPrintToolListBody_SortsBySizeDescendingWhenRequested(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")
	// Deliberately the SMALLER one is also the NEWER version number,
	// so newest-first and size-desc genuinely disagree -- a test that
	// happened to have them agree wouldn't prove anything.
	writeFakeFile(t, filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+"21.0.2-temurin", "bin", "java"), 10*1024*1024)
	writeFakeFile(t, filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+"17.0.9-temurin", "bin", "java"), 200*1024*1024)

	out := captureStdout(t, func() {
		versions := mustScan(t, tool)
		sizes, total := computeSizes(versions)
		printToolListBody(tool, versions, sizes, total, 0, true)
	})
	bigIdx := strings.Index(out, "17.0.9-temurin")
	smallIdx := strings.Index(out, "21.0.2-temurin")
	if bigIdx == -1 || smallIdx == -1 {
		t.Fatalf("expected both versions present, got: %q", out)
	}
	if bigIdx > smallIdx {
		t.Errorf("expected the 200MB version (17.0.9) to print BEFORE the 10MB version (21.0.2) with --sizes, got: %q", out)
	}
}

// TestPrintToolListBody_NoSortWhenSizesNotRequested confirms the
// default, no-flag behavior is completely unchanged -- newest-first,
// exactly as inventory.Scan already returns it.
func TestPrintToolListBody_NoSortWhenSizesNotRequested(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")
	installFakeJava(t, home, "21.0.2-temurin")
	installFakeJava(t, home, "17.0.9-temurin")

	out := captureStdout(t, func() {
		versions := mustScan(t, tool)
		printToolListBody(tool, versions, nil, 0, 0, false)
	})
	// inventory.Scan's own newest-first order -- not asserting the
	// exact order here (that's inventory's own contract, tested
	// elsewhere), only that it's UNCHANGED from before this feature
	// existed, i.e. NOT re-sorted by any size-related logic.
	firstIdx := strings.Index(out, "21.0.2-temurin")
	secondIdx := strings.Index(out, "17.0.9-temurin")
	if firstIdx == -1 || secondIdx == -1 || firstIdx > secondIdx {
		t.Errorf("expected the normal newest-first order preserved without --sizes, got: %q", out)
	}
}

// TestPrintVersions_SizeColumnIsRightAlignedAndColumnOrderIsCorrect
// confirms improvements #1 and #2 together: the size text for a
// SHORTER number is left-padded to match a LONGER one in the same
// listing (right-alignment), and version/size/path appear in that
// order on the line.
func TestPrintVersions_SizeColumnIsRightAlignedAndColumnOrderIsCorrect(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")
	writeFakeFile(t, filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+"21.0.2-temurin", "bin", "java"), 5*1024*1024)   // "5.0 MB"
	writeFakeFile(t, filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+"17.0.9-temurin", "bin", "java"), 180*1024*1024) // "180.0 MB"

	out := captureStdout(t, func() {
		versions := mustScan(t, tool)
		sizes, total := computeSizes(versions)
		printToolListBody(tool, versions, sizes, total, 0, true)
	})

	var line5MB, line180MB string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "21.0.2-temurin") {
			line5MB = line
		}
		if strings.Contains(line, "17.0.9-temurin") {
			line180MB = line
		}
	}
	if line5MB == "" || line180MB == "" {
		t.Fatalf("expected both lines present, got: %q", out)
	}

	// Column order: version index < size index < path index.
	for _, line := range []string{line5MB, line180MB} {
		vIdx := strings.Index(line, "temurin")
		sIdx := strings.Index(line, "MB")
		pIdx := strings.Index(line, tool.CandidateRoot())
		if !(vIdx < sIdx && sIdx < pIdx) {
			t.Errorf("expected version < size < path column order, got indices v=%d s=%d p=%d in line: %q", vIdx, sIdx, pIdx, line)
		}
	}

	// Right alignment: "5.0 MB" (6 chars) must be left-padded to match
	// "180.0 MB" (8 chars) -- the text immediately before "5.0 MB"
	// should include extra leading space(s) that "180.0 MB" doesn't
	// have before it, so both "MB"s end at the same column.
	mbEnd5 := strings.Index(line5MB, "5.0 MB") + len("5.0 MB")
	mbEnd180 := strings.Index(line180MB, "180.0 MB") + len("180.0 MB")
	if mbEnd5 != mbEnd180 {
		t.Errorf("expected both size columns to END at the same character position (right-aligned), got %d vs %d in:\n%q\n%q", mbEnd5, mbEnd180, line5MB, line180MB)
	}
}

// TestPrintToolListBody_PercentageShownOnlyWhenGrandTotalProvided
// confirms improvement #5: a nonzero grandTotal (the all-tools case)
// adds a "(X% of grand total)" suffix; grandTotal=0 (the single-tool
// case, where there's nothing meaningful to compare against) omits
// it entirely, not "(100%)" or any other placeholder.
func TestPrintToolListBody_PercentageShownOnlyWhenGrandTotalProvided(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")
	writeFakeFile(t, filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+"21.0.2-temurin", "bin", "java"), 25*1024*1024)

	withoutGrandTotal := captureStdout(t, func() {
		versions := mustScan(t, tool)
		sizes, total := computeSizes(versions)
		printToolListBody(tool, versions, sizes, total, 0, true)
	})
	if strings.Contains(withoutGrandTotal, "% of grand total") {
		t.Errorf("expected no percentage when grandTotal=0, got: %q", withoutGrandTotal)
	}

	withGrandTotal := captureStdout(t, func() {
		versions := mustScan(t, tool)
		sizes, total := computeSizes(versions)
		printToolListBody(tool, versions, sizes, total, 100*1024*1024, true) // this tool is 25% of a 100MB grand total
	})
	if !strings.Contains(withGrandTotal, "(25% of grand total)") {
		t.Errorf("expected the exact percentage line, got: %q", withGrandTotal)
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

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sdkkeeper/internal/inventory"
	"sdkkeeper/internal/picker"
	"sdkkeeper/internal/tooldef"
)

func TestVersionVendor_RecognizesKnownSuffix(t *testing.T) {
	knownVendors := []string{"temurin", "liberica"}

	cases := map[string]string{
		"21.0.9-temurin":    "temurin",
		"26.0.2.1-liberica": "liberica",
	}
	for number, want := range cases {
		got, ok := versionVendor(number, knownVendors)
		if !ok {
			t.Errorf("expected %q to be recognized, got no match", number)
			continue
		}
		if got != want {
			t.Errorf("versionVendor(%q) = %q, want %q", number, got, want)
		}
	}
}

func TestVersionVendor_RejectsUnrecognizedSuffix(t *testing.T) {
	knownVendors := []string{"temurin", "liberica"}

	// A regression case for exactly the scenario that motivated
	// deliberately NOT applying this same grouping to the not-managed
	// section: a label that merely LOOKS like a known vendor name
	// (someone could type anything via `add`) is a case this function
	// itself correctly still matches syntactically -- the actual
	// safeguard is that list.go only ever calls this for the managed
	// section, where the suffix is trustworthy because sk itself
	// generated it, not user input.
	cases := []string{
		"21.0.9",          // bare, no suffix at all (pre-vendor-suffix era)
		"21.0.9-corretto", // a genuinely unrecognized vendor
		"21.0.9-temurinx", // similar but not an exact match
	}
	for _, number := range cases {
		if _, ok := versionVendor(number, knownVendors); ok {
			t.Errorf("expected %q to NOT match any known vendor, but it did", number)
		}
	}
}

func TestVersionVendor_EmptyKnownVendorsNeverMatches(t *testing.T) {
	if _, ok := versionVendor("21.0.9-temurin", nil); ok {
		t.Error("expected no match when there are no known vendors to check against")
	}
}

func TestBuildPickerGroups_MultiVendorGroupsByVendorMatchingListOrder(t *testing.T) {
	tool, ok := tooldef.Get("java")
	if !ok {
		t.Fatal("java not registered")
	}
	versions := []inventory.Version{
		{Number: "17.0.3-temurin"},
		{Number: "21.0.2-temurin"},
		{Number: "26.0.2-liberica"},
	}
	groups := buildPickerGroups(tool, versions)

	if len(groups) != 2 {
		t.Fatalf("expected 2 groups (Temurin, Liberica), got %d: %+v", len(groups), groups)
	}
	if groups[0].Header != "Temurin" || len(groups[0].Items) != 2 {
		t.Errorf("expected Temurin group with 2 items first, got: %+v", groups[0])
	}
	if groups[1].Header != "Liberica" || len(groups[1].Items) != 1 {
		t.Errorf("expected Liberica group with 1 item second, got: %+v", groups[1])
	}
}

// TestBuildPickerGroups_SingleVendorProducesNoHeader confirms
// single-vendor tools (Maven, Gradle) get a flat, header-less group --
// matching printManagedGroup's own identical rule, and keeping their
// picker experience completely unchanged by this feature.
func TestBuildPickerGroups_SingleVendorProducesNoHeader(t *testing.T) {
	tool, ok := tooldef.Get("maven")
	if !ok {
		t.Fatal("maven not registered")
	}
	versions := []inventory.Version{{Number: "3.9.9"}, {Number: "3.8.8"}}
	groups := buildPickerGroups(tool, versions)

	if len(groups) != 1 {
		t.Fatalf("expected exactly 1 group, got %d: %+v", len(groups), groups)
	}
	if groups[0].Header != "" {
		t.Errorf("expected no header for a single-vendor tool, got: %q", groups[0].Header)
	}
	if len(groups[0].Items) != 2 {
		t.Errorf("expected both versions in the single group, got: %+v", groups[0].Items)
	}
}

func TestBuildPickerGroups_UnrecognizedSuffixFallsIntoOther(t *testing.T) {
	tool, ok := tooldef.Get("java")
	if !ok {
		t.Fatal("java not registered")
	}
	versions := []inventory.Version{
		{Number: "21.0.2-temurin"},
		{Number: "21.0.9-corretto"}, // unrecognized vendor
	}
	groups := buildPickerGroups(tool, versions)

	var otherGroup *picker.Group
	for i := range groups {
		if groups[i].Header == "Other" {
			otherGroup = &groups[i]
		}
	}
	if otherGroup == nil {
		t.Fatalf("expected an 'Other' group for the unrecognized suffix, got: %+v", groups)
	}
	if len(otherGroup.Items) != 1 || otherGroup.Items[0] != "21.0.9-corretto" {
		t.Errorf("expected 'Other' to contain exactly the unrecognized entry, got: %+v", otherGroup.Items)
	}
}

func TestBuildPickerGroups_ExternalEntriesGetOwnNotManagedGroup(t *testing.T) {
	tool, ok := tooldef.Get("java")
	if !ok {
		t.Fatal("java not registered")
	}
	versions := []inventory.Version{
		{Number: "21.0.2-temurin"},
		{Number: "25.0.1", External: true},
	}
	groups := buildPickerGroups(tool, versions)

	var notManaged *picker.Group
	for i := range groups {
		if groups[i].Header == "Not managed" {
			notManaged = &groups[i]
		}
	}
	if notManaged == nil {
		t.Fatalf("expected a 'Not managed' group for the external entry, got: %+v", groups)
	}
	if len(notManaged.Items) != 1 || notManaged.Items[0] != "25.0.1" {
		t.Errorf("expected 'Not managed' to contain exactly the external entry, got: %+v", notManaged.Items)
	}
}

// TestPrintManagedGroup_VendorsShareIdenticalColumnAlignment is a
// regression test for a real, live-reported bug: Temurin's and
// Liberica's version listings, printed as two separate calls, had
// their path columns start at visibly different horizontal
// positions whenever one vendor's longest version number happened to
// be a different length than the other's -- confirmed directly, by
// measuring the exact byte offset the path began at on a real,
// reported screenshot's data shape (reproduced here with the same
// version numbers). Two distinct fixes were needed and are both
// covered here: sharing one total width across both vendors' tables
// wasn't sufficient on its own (lipgloss/table redistributes column
// boundaries per call, even given an identical total); the working
// fix abandoned lipgloss/table for this specific function entirely,
// back to plain, fixed-width padding, which has no such ambiguity.
func TestPrintManagedGroup_VendorsShareIdenticalColumnAlignment(t *testing.T) {
	tool, ok := tooldef.Get("java")
	if !ok {
		t.Fatal("java not registered")
	}
	versions := []inventory.Version{
		{Number: "26.0.2.1-temurin", Path: "/x/JDK-26.0.2.1-temurin"},
		{Number: "17.0.20.1-temurin", Path: "/x/JDK-17.0.20.1-temurin"},
		{Number: "16.0.2-temurin", Path: "/x/JDK-16.0.2-temurin"},
		{Number: "24.0.2-liberica", Path: "/x/JDK-24.0.2-liberica"},
		{Number: "22.0.1-liberica", Path: "/x/JDK-22.0.1-liberica"},
		{Number: "19-liberica", Path: "/x/JDK-19-liberica"},
	}
	annotate := func(v inventory.Version) (string, string) { return "", "" }

	out := captureStdout(t, func() {
		printManagedGroup(tool, "Managed by SDK Keeper:", versions, annotate)
	})

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	var pathOffsets []int
	for _, line := range lines {
		if idx := strings.Index(line, "/x/JDK-"); idx != -1 {
			pathOffsets = append(pathOffsets, idx)
		}
	}
	if len(pathOffsets) != len(versions) {
		t.Fatalf("expected %d path lines, found %d in output:\n%s", len(versions), len(pathOffsets), out)
	}
	first := pathOffsets[0]
	for i, offset := range pathOffsets {
		if offset != first {
			t.Errorf("line %d: path starts at column %d, expected %d (all versions, across BOTH vendors, must align identically) -- full output:\n%s", i, offset, first, out)
		}
	}
}

func TestBuildListJSON_UnknownToolIsAmbiguousTool(t *testing.T) {
	setTestHome(t, t.TempDir())
	_, jerr := buildListJSON("not-a-real-tool")
	if jerr == nil || jerr.Code != ErrCodeAmbiguousTool {
		t.Fatalf("expected ambiguous_tool, got %+v", jerr)
	}
}

// TestBuildListJSON_EmptyInventoryReportsEmptyArrayNotNull confirms
// the JSON output uses [] for "nothing installed", not a bare JSON
// null -- an important distinction for any caller that unconditionally
// iterates data.installed without a nil-check first.
func TestBuildListJSON_EmptyInventoryReportsEmptyArrayNotNull(t *testing.T) {
	setTestHome(t, t.TempDir())
	data, jerr := buildListJSON("java")
	if jerr != nil {
		t.Fatalf("expected success, got error: %+v", jerr)
	}
	if data.Installed == nil {
		t.Error("expected a non-nil (empty) slice, got nil -- would serialize as JSON null instead of []")
	}
	if len(data.Installed) != 0 {
		t.Errorf("expected zero entries, got %d", len(data.Installed))
	}
}

// TestBuildListJSON_ReportsVendorDefaultAndCurrentCorrectly is the
// main happy-path/invariant test: one installed, multi-vendor version
// that is BOTH the stored default AND the currently-active one (via
// the tool's real env var) must report vendor, isDefault, and
// isCurrent all correctly and simultaneously.
func TestBuildListJSON_ReportsVendorDefaultAndCurrentCorrectly(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")
	tool, _ := tooldef.Get("java")
	dir := filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+"21.0.2-temurin")

	if err := os.MkdirAll(filepath.Dir(tool.DefaultPath()), 0o755); err != nil {
		t.Fatalf("setup (defaults dir) failed: %v", err)
	}
	if err := os.WriteFile(tool.DefaultPath(), []byte("21.0.2-temurin"), 0o644); err != nil {
		t.Fatalf("setup (default file) failed: %v", err)
	}

	t.Setenv(tool.EnvVar, tool.HomePath(dir))

	data, jerr := buildListJSON("java")
	if jerr != nil {
		t.Fatalf("expected success, got error: %+v", jerr)
	}
	if len(data.Installed) != 1 {
		t.Fatalf("expected exactly 1 installed entry, got %d: %+v", len(data.Installed), data.Installed)
	}
	entry := data.Installed[0]
	if entry.Vendor == nil || *entry.Vendor != "temurin" {
		t.Errorf("expected vendor=temurin, got %v", entry.Vendor)
	}
	if !entry.IsDefault {
		t.Error("expected isDefault=true")
	}
	if !entry.IsCurrent {
		t.Error("expected isCurrent=true")
	}
}

// TestBuildListJSON_SingleVendorToolAlwaysReportsNilVendor confirms
// design doc §9 (vendor is null for single-vendor tools) applies to
// list's own installed entries too, not just use/remove/default's.
func TestBuildListJSON_SingleVendorToolAlwaysReportsNilVendor(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeMaven(t, home, "3.9.9")

	data, jerr := buildListJSON("maven")
	if jerr != nil {
		t.Fatalf("expected success, got error: %+v", jerr)
	}
	if len(data.Installed) != 1 {
		t.Fatalf("expected exactly 1 installed entry, got %d", len(data.Installed))
	}
	if data.Installed[0].Vendor != nil {
		t.Errorf("expected nil vendor for single-vendor maven, got %v", *data.Installed[0].Vendor)
	}
}

package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"sdkkeeper/internal/tooldef"
)

// TestJSONConsistency_ListCurrentAgreesWithCurrentCommand is design
// doc §10's own named invariant: "list's isCurrent:true entry must
// always agree with current's active value for the same tool." Both
// buildListJSON and buildCurrentJSON independently call
// findActiveVersion against the same inventory.Scan/os.LookupEnv
// inputs -- this test exercises them side by side, against the same
// real on-disk state, to confirm they can never silently diverge.
func TestJSONConsistency_ListCurrentAgreesWithCurrentCommand(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")
	installFakeJava(t, home, "17.0.9-liberica")
	tool, _ := tooldef.Get("java")
	activeDir := filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+"17.0.9-liberica")
	t.Setenv(tool.EnvVar, tool.HomePath(activeDir))

	listData, jerr := buildListJSON("java")
	if jerr != nil {
		t.Fatalf("list failed: %+v", jerr)
	}
	currentData, jerr := buildCurrentJSON("java")
	if jerr != nil {
		t.Fatalf("current failed: %+v", jerr)
	}

	if currentData.Active == nil {
		t.Fatal("expected current to report something active")
	}

	var listSaysCurrent string
	currentCount := 0
	for _, entry := range listData.Installed {
		if entry.IsCurrent {
			currentCount++
			listSaysCurrent = entry.Version
		}
	}
	if currentCount != 1 {
		t.Fatalf("expected exactly one isCurrent:true entry in list, got %d", currentCount)
	}
	if listSaysCurrent != currentData.Active.Version {
		t.Errorf("list.installed[isCurrent].version (%q) disagrees with current.active.version (%q)", listSaysCurrent, currentData.Active.Version)
	}

	// The OTHER installed version must be reported as NOT current by
	// list -- confirms this isn't a trivially-true test (e.g. both
	// sides always returning the same hardcoded value).
	for _, entry := range listData.Installed {
		if entry.Version != currentData.Active.Version && entry.IsCurrent {
			t.Errorf("expected %q to be reported as NOT current, but list marked it isCurrent:true", entry.Version)
		}
	}
}

// TestJSONConsistency_NothingActiveAgreesAcrossBoth confirms the
// negative case of the same invariant: with nothing active, list must
// report isCurrent:false for every entry, matching current's own
// active:null.
func TestJSONConsistency_NothingActiveAgreesAcrossBoth(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")
	os.Unsetenv("JAVA_HOME")

	listData, jerr := buildListJSON("java")
	if jerr != nil {
		t.Fatalf("list failed: %+v", jerr)
	}
	currentData, jerr := buildCurrentJSON("java")
	if jerr != nil {
		t.Fatalf("current failed: %+v", jerr)
	}

	if currentData.Active != nil {
		t.Fatalf("expected current.active = nil, got %+v", currentData.Active)
	}
	for _, entry := range listData.Installed {
		if entry.IsCurrent {
			t.Errorf("expected no entry to be isCurrent:true when nothing is active, got %+v", entry)
		}
	}
}

// TestJSONConsistency_SearchIsInstalledAgreesWithListInstalledSet is
// design doc §10's second named invariant: "search's isInstalled must
// always agree with what list reports as installed." Both
// buildListJSON and buildSearchPatchesJSON derive their notion of
// "installed" from the exact same inventory.Scan call -- this
// confirms a version list reports as installed is ALSO the one search
// marks isInstalled:true, and a version list does NOT know about is
// NOT marked installed by search either.
func TestJSONConsistency_SearchIsInstalledAgreesWithListInstalledSet(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")
	tool, _ := tooldef.Get("java")

	listData, jerr := buildListJSON("java")
	if jerr != nil {
		t.Fatalf("list failed: %+v", jerr)
	}
	listInstalled := make(map[string]bool, len(listData.Installed))
	for _, entry := range listData.Installed {
		listInstalled[entry.Version] = true
	}

	provider := mockProvider{
		name:    "temurin",
		patches: []string{"21.0.2", "21.0.1"}, // one installed (via vendor-suffixed match), one not
	}
	searchData, jerr := buildSearchPatchesJSON(context.Background(), tool, provider, "21")
	if jerr != nil {
		t.Fatalf("search failed: %+v", jerr)
	}

	for _, entry := range searchData.Available {
		// search's bare patch numbers ("21.0.2") map to list's
		// vendor-suffixed version numbers ("21.0.2-temurin") via the
		// same provider.Name() suffix both searchPatches (text) and
		// buildSearchPatchesJSON (JSON) already apply -- reconstruct
		// that same mapping here rather than assuming a bare-string
		// match, so this test reflects the real, documented
		// vendor-suffix convention (design doc §9), not a
		// coincidental one.
		suffixed := entry.Version + "-" + provider.Name()
		wantInstalled := listInstalled[suffixed]
		if entry.IsInstalled != wantInstalled {
			t.Errorf("search reports isInstalled=%v for %q, but list's installed set says %v", entry.IsInstalled, entry.Version, wantInstalled)
		}
	}
}

package cli

import (
	"os"
	"testing"

	"sdkkeeper/internal/tooldef"
)

func TestResolveDefaultJSON_UnknownToolIsAmbiguousTool(t *testing.T) {
	setTestHome(t, t.TempDir())
	_, jerr := resolveDefaultJSON("not-a-real-tool", "")
	if jerr == nil || jerr.Code != ErrCodeAmbiguousTool {
		t.Fatalf("expected ambiguous_tool, got %+v", jerr)
	}
}

// TestResolveDefaultJSON_ShowWithNoneSetIsNotFound confirms the
// "show" shape (empty versionArg) with nothing ever set reports
// not_found -- there's genuinely nothing to report, distinct from a
// version_required case (no version argument is EXPECTED for "show").
func TestResolveDefaultJSON_ShowWithNoneSetIsNotFound(t *testing.T) {
	setTestHome(t, t.TempDir())
	_, jerr := resolveDefaultJSON("java", "")
	if jerr == nil || jerr.Code != ErrCodeNotFound {
		t.Fatalf("expected not_found, got %+v", jerr)
	}
}

// TestResolveDefaultJSON_SetThenShowRoundTrips is the main happy
// path: setting a default, then showing it, both report the same
// version/vendor under the "defaulted" action.
func TestResolveDefaultJSON_SetThenShowRoundTrips(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")

	setData, jerr := resolveDefaultJSON("java", "21.0.2-temurin")
	if jerr != nil {
		t.Fatalf("expected success setting the default, got error: %+v", jerr)
	}
	if setData.Action != string(actionDefaulted) {
		t.Errorf("expected action=defaulted, got %q", setData.Action)
	}
	if setData.Vendor == nil || *setData.Vendor != "temurin" {
		t.Errorf("expected vendor=temurin, got %v", setData.Vendor)
	}

	showData, jerr := resolveDefaultJSON("java", "")
	if jerr != nil {
		t.Fatalf("expected success showing the default, got error: %+v", jerr)
	}
	if showData.Version != "21.0.2-temurin" {
		t.Errorf("expected the just-set version when showing, got %q", showData.Version)
	}
	if showData.Action != string(actionDefaulted) {
		t.Errorf("expected action=defaulted for the show case too, got %q", showData.Action)
	}
}

// TestResolveDefaultJSON_SetRejectsUninstalledVersion confirms
// design doc's own "validated against what's actually installed
// FIRST" principle (matching the interactive path) -- a typo doesn't
// silently succeed.
func TestResolveDefaultJSON_SetRejectsUninstalledVersion(t *testing.T) {
	setTestHome(t, t.TempDir())
	_, jerr := resolveDefaultJSON("java", "99.0.0-temurin")
	if jerr == nil || jerr.Code != ErrCodeNotFound {
		t.Fatalf("expected not_found for an uninstalled version, got %+v", jerr)
	}
}

// TestResolveDefaultJSON_NullClearsAndReportsDefaultCleared confirms
// the "null" clearing shape, including the real, on-disk side effect
// (clearDefaultFile actually removing the file), not just the
// reported action.
func TestResolveDefaultJSON_NullClearsAndReportsDefaultCleared(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")
	if _, jerr := resolveDefaultJSON("java", "21.0.2-temurin"); jerr != nil {
		t.Fatalf("setup (setting the default) failed: %+v", jerr)
	}

	data, jerr := resolveDefaultJSON("java", "null")
	if jerr != nil {
		t.Fatalf("expected success clearing the default, got error: %+v", jerr)
	}
	if data.Action != string(actionDefaultCleared) {
		t.Errorf("expected action=default_cleared, got %q", data.Action)
	}
	if data.Vendor != nil {
		t.Errorf("expected nil vendor when clearing, got %v", *data.Vendor)
	}

	tool, _ := tooldef.Get("java")
	if _, err := os.Stat(tool.DefaultPath()); !os.IsNotExist(err) {
		t.Errorf("expected the default file to actually be removed from disk, got Stat error: %v", err)
	}
}

// TestResolveDefaultJSON_ClearingWithNothingSetStillSucceeds mirrors
// clearDefaultFile's own idempotent-removal contract (see
// clearDefaultFile's doc comment: "Returns nil if the file was
// already gone") -- clearing an already-unset default is not an
// error under --format=json either.
func TestResolveDefaultJSON_ClearingWithNothingSetStillSucceeds(t *testing.T) {
	setTestHome(t, t.TempDir())
	data, jerr := resolveDefaultJSON("java", "null")
	if jerr != nil {
		t.Fatalf("expected success, got error: %+v", jerr)
	}
	if data.Action != string(actionDefaultCleared) {
		t.Errorf("expected action=default_cleared, got %q", data.Action)
	}
}

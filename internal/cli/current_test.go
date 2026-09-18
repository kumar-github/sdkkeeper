package cli

import (
	"os"
	"path/filepath"
	"testing"

	"sdkkeeper/internal/inventory"
	"sdkkeeper/internal/tooldef"
)

func TestFindActiveVersion_MatchFound(t *testing.T) {
	tool, _ := tooldef.Get("maven") // no darwin-specific HomePath transform, simpler to reason about
	versions := []inventory.Version{
		{Number: "3.9.14", Path: "/home/user/.sdkkeeper/candidates/maven/apache-maven-3.9.14"},
		{Number: "3.8.1", Path: "/home/user/.sdkkeeper/candidates/maven/apache-maven-3.8.1"},
	}

	v, ok := findActiveVersion(tool, versions, "/home/user/.sdkkeeper/candidates/maven/apache-maven-3.8.1")
	if !ok {
		t.Fatal("expected a match to be found")
	}
	if v.Number != "3.8.1" {
		t.Errorf("expected the matching version 3.8.1, got: %q", v.Number)
	}
}

func TestFindActiveVersion_NoMatch(t *testing.T) {
	tool, _ := tooldef.Get("maven")
	versions := []inventory.Version{
		{Number: "3.9.14", Path: "/home/user/.sdkkeeper/candidates/maven/apache-maven-3.9.14"},
	}

	_, ok := findActiveVersion(tool, versions, "/some/manually/set/path")
	if ok {
		t.Error("expected no match for a value that doesn't correspond to any known version")
	}
}

func TestFindActiveVersion_EmptyVersionList(t *testing.T) {
	tool, _ := tooldef.Get("maven")
	_, ok := findActiveVersion(tool, nil, "/anything")
	if ok {
		t.Error("expected no match when there are no installed versions at all")
	}
}

// TestFindActiveVersion_JavaContentsHomeTransform confirms the
// matching correctly accounts for java's own darwin-specific
// Contents/Home transformation (HomePath), not just a raw path
// comparison -- the actual JAVA_HOME value always includes this
// suffix on macOS, so matching against the bare version directory
// alone would never succeed.
func TestFindActiveVersion_JavaContentsHomeTransform(t *testing.T) {
	tool, _ := tooldef.Get("java")
	versionDir := "/home/user/.sdkkeeper/candidates/java/JDK-21.0.2-temurin"
	versions := []inventory.Version{
		{Number: "21.0.2-temurin", Path: versionDir},
	}

	realEnvValue := tool.HomePath(versionDir)
	v, ok := findActiveVersion(tool, versions, realEnvValue)
	if !ok {
		t.Fatalf("expected a match using the real, transformed HomePath value (%q)", realEnvValue)
	}
	if v.Number != "21.0.2-temurin" {
		t.Errorf("expected 21.0.2-temurin, got: %q", v.Number)
	}
}

func TestBuildCurrentJSON_UnknownToolIsAmbiguousTool(t *testing.T) {
	setTestHome(t, t.TempDir())
	_, jerr := buildCurrentJSON("not-a-real-tool")
	if jerr == nil || jerr.Code != ErrCodeAmbiguousTool {
		t.Fatalf("expected ambiguous_tool, got %+v", jerr)
	}
}

// TestBuildCurrentJSON_NothingSetReportsActiveNull confirms design doc
// §3's own explicit "active: null is a valid, non-error state" --
// this must be a SUCCESS response, never a jsonError.
func TestBuildCurrentJSON_NothingSetReportsActiveNull(t *testing.T) {
	setTestHome(t, t.TempDir())
	os.Unsetenv("JAVA_HOME")
	data, jerr := buildCurrentJSON("java")
	if jerr != nil {
		t.Fatalf("expected success with active:null, got error: %+v", jerr)
	}
	if data.Active != nil {
		t.Errorf("expected Active to be nil, got %+v", data.Active)
	}
}

// TestBuildCurrentJSON_RealMatchReportsVersionAndVendor is the main
// happy path -- and, together with list_test.go's own
// TestBuildListJSON_ReportsVendorDefaultAndCurrentCorrectly, is half
// of the cross-command invariant design doc §3 calls out directly
// (see json_consistency_test.go for the explicit side-by-side check).
func TestBuildCurrentJSON_RealMatchReportsVersionAndVendor(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-liberica")
	tool, _ := tooldef.Get("java")
	dir := filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+"21.0.2-liberica")
	t.Setenv(tool.EnvVar, tool.HomePath(dir))

	data, jerr := buildCurrentJSON("java")
	if jerr != nil {
		t.Fatalf("expected success, got error: %+v", jerr)
	}
	if data.Active == nil {
		t.Fatal("expected a non-nil Active")
	}
	if data.Active.Version != "21.0.2-liberica" {
		t.Errorf("expected version=21.0.2-liberica, got %q", data.Active.Version)
	}
	if data.Active.Vendor == nil || *data.Active.Vendor != "liberica" {
		t.Errorf("expected vendor=liberica, got %v", data.Active.Vendor)
	}
}

// TestBuildCurrentJSON_UnrecognizedEnvValueReportsActiveNull confirms
// the deliberate simplification documented in buildCurrentJSON's own
// doc comment: an env var that's set but doesn't match anything sk
// recognizes reports active:null (the schema has no third shape for
// "raw, unrecognized value"), rather than the interactive text path's
// richer "does not match any version..." message.
func TestBuildCurrentJSON_UnrecognizedEnvValueReportsActiveNull(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")
	t.Setenv(tool.EnvVar, "/some/manually/set/path/nothing/knows/about")

	data, jerr := buildCurrentJSON("java")
	if jerr != nil {
		t.Fatalf("expected success, got error: %+v", jerr)
	}
	if data.Active != nil {
		t.Errorf("expected Active to be nil for an unrecognized env value, got %+v", data.Active)
	}
}

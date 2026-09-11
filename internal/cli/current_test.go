package cli

import (
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

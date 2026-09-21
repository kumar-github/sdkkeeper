package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sdkkeeper/internal/tooldef"
)

func TestRunShowSkrc_NoSkrcFoundPrintsPlainMessageNoError(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	chdir(t, home)

	var runErr error
	out := withSession(t, func() {
		runErr = runShowSkrc()
	})
	if runErr != nil {
		t.Fatalf("expected no error when no .skrc exists, got: %v", runErr)
	}
	if !strings.Contains(out, "No .skrc found") {
		t.Errorf("expected a 'no .skrc found' message, got: %q", out)
	}
}

func TestRunShowSkrc_ReportsPathAndPerEntryStatus(t *testing.T) {
	home := realpath(t, t.TempDir())
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")
	installFakeMaven(t, home, "3.9.9")

	javaTool, _ := tooldef.Get("java")
	javaDir := filepath.Join(javaTool.CandidateRoot(), javaTool.FolderPrefix+"21.0.2-temurin")
	t.Setenv("JAVA_HOME", javaTool.HomePath(javaDir))

	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	skrcPath := writeSkrc(t, proj, "java=21.0.2-temurin\nmaven=3.9.9\ngradle=8.5\n")
	chdir(t, proj)

	var runErr error
	out := withSession(t, func() {
		runErr = runShowSkrc()
	})
	if runErr != nil {
		t.Fatalf("expected no error, got: %v", runErr)
	}
	if !strings.Contains(out, skrcPath) {
		t.Errorf("expected the resolved path %q in output, got: %q", skrcPath, out)
	}
	if !strings.Contains(out, "installed, active") {
		t.Errorf("expected java to report installed+active, got: %q", out)
	}
	if !strings.Contains(out, "installed, not active") {
		t.Errorf("expected maven to report installed but not active, got: %q", out)
	}
	if !strings.Contains(out, "not installed") {
		t.Errorf("expected gradle to report not installed, got: %q", out)
	}
}

func TestRunShowSkrc_UnknownToolReportedDistinctly(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	writeSkrc(t, proj, "notarealtool=1.0\n")
	chdir(t, proj)

	var runErr error
	out := withSession(t, func() {
		runErr = runShowSkrc()
	})
	if runErr != nil {
		t.Fatalf("expected no error (this is a report, not a validation), got: %v", runErr)
	}
	if !strings.Contains(out, "unknown tool") {
		t.Errorf("expected an unknown-tool line, got: %q", out)
	}
}

func TestRunShowSkrc_EmptySkrcReportsNoCandidates(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	writeSkrc(t, proj, "# nothing was active\n")
	chdir(t, proj)

	var runErr error
	out := withSession(t, func() {
		runErr = runShowSkrc()
	})
	if runErr != nil {
		t.Fatalf("expected no error, got: %v", runErr)
	}
	if !strings.Contains(out, "no candidates listed") {
		t.Errorf("expected a no-candidates message, got: %q", out)
	}
}

func TestBuildSkrcJSON_NotFoundReportsNullPathEmptyEntries(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	chdir(t, home)

	data, jerr := buildSkrcJSON()
	if jerr != nil {
		t.Fatalf("expected success, got error: %+v", jerr)
	}
	if data.Path != nil {
		t.Errorf("expected a nil path, got %q", *data.Path)
	}
	if len(data.Entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(data.Entries))
	}
}

func TestBuildSkrcJSON_ReportsPathAndPerEntryStatus(t *testing.T) {
	home := realpath(t, t.TempDir())
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")

	javaTool, _ := tooldef.Get("java")
	javaDir := filepath.Join(javaTool.CandidateRoot(), javaTool.FolderPrefix+"21.0.2-temurin")
	t.Setenv("JAVA_HOME", javaTool.HomePath(javaDir))

	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	skrcPath := writeSkrc(t, proj, "java=21.0.2-temurin\nmaven=3.9.9\nnotarealtool=1.0\n")
	chdir(t, proj)

	data, jerr := buildSkrcJSON()
	if jerr != nil {
		t.Fatalf("expected success, got error: %+v", jerr)
	}
	if data.Path == nil || *data.Path != skrcPath {
		got := "<nil>"
		if data.Path != nil {
			got = *data.Path
		}
		t.Fatalf("expected path %q, got %q", skrcPath, got)
	}
	if len(data.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(data.Entries), data.Entries)
	}

	byTool := map[string]skrcEntryPayload{}
	for _, e := range data.Entries {
		byTool[e.Tool] = e
	}

	java := byTool["java"]
	if !java.Known || !java.Installed || !java.Active {
		t.Errorf("expected java known+installed+active, got %+v", java)
	}
	maven := byTool["maven"]
	if !maven.Known || maven.Installed || maven.Active {
		t.Errorf("expected maven known, not installed, not active, got %+v", maven)
	}
	unknown := byTool["notarealtool"]
	if unknown.Known || unknown.Installed || unknown.Active {
		t.Errorf("expected notarealtool entirely false (Known=false), got %+v", unknown)
	}
}

// TestSkrcCmd_JSONMatchesBuildSkrcJSON confirms the cobra wiring calls
// buildSkrcJSON (not some separate, potentially-diverging path) and
// that the envelope is well-formed.
func TestSkrcCmd_JSONMatchesBuildSkrcJSON(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	chdir(t, home)

	oldFormat := outputFormat
	outputFormat = FormatJSON
	defer func() { outputFormat = oldFormat }()

	var runErr error
	out := captureStdout(t, func() {
		cmd := newSkrcCmd()
		runErr = cmd.RunE(cmd, []string{})
	})
	if runErr != nil {
		t.Fatalf("expected success, got: %v", runErr)
	}
	if !strings.Contains(out, `"status":"ok"`) {
		t.Errorf("expected a success envelope, got: %q", out)
	}
	if !strings.Contains(out, `"path":null`) {
		t.Errorf("expected path:null when no .skrc exists, got: %q", out)
	}
}

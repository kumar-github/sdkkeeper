package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// captureVersionJSON runs newVersionCmd's RunE under --format=json
// and returns the raw stdout it wrote -- writeJSONEnvelope always
// targets os.Stdout directly (never session.Out), so capturing that
// is the only way to observe it.
func captureVersionJSON(t *testing.T, version, commit string) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stdout = w

	cmd := newVersionCmd(version, commit)
	if err := cmd.RunE(cmd, nil); err != nil {
		os.Stdout = old
		t.Fatalf("newVersionCmd RunE returned an unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String()
}

// TestVersionJSON_DevBuildOmitsCommitAndGoVersion confirms design doc
// §3's convention: a local, unreleased "dev" build reports plainly,
// without a commit or Go-toolchain version it never actually pinned.
func TestVersionJSON_DevBuildOmitsCommitAndGoVersion(t *testing.T) {
	old := outputFormat
	outputFormat = FormatJSON
	defer func() { outputFormat = old }()

	out := captureVersionJSON(t, "dev", "")

	var env struct {
		SchemaVersion int    `json:"schemaVersion"`
		Status        string `json:"status"`
		Data          struct {
			Version   string `json:"version"`
			Commit    string `json:"commit"`
			GoVersion string `json:"goVersion"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("expected valid JSON, got %q: %v", out, err)
	}
	if env.SchemaVersion != schemaVersion || env.Status != "ok" {
		t.Errorf("expected a standard ok envelope, got schemaVersion=%d status=%q", env.SchemaVersion, env.Status)
	}
	if env.Data.Version != "dev" {
		t.Errorf("expected version=dev, got %q", env.Data.Version)
	}
	if env.Data.Commit != "" || env.Data.GoVersion != "" {
		t.Errorf("expected commit and goVersion both omitted for a dev build, got commit=%q goVersion=%q", env.Data.Commit, env.Data.GoVersion)
	}
}

// TestVersionJSON_ReleaseBuildIncludesCommitAndGoVersion confirms a
// real, ldflags-injected version populates both commit (as injected)
// and goVersion (from runtime.Version(), needing no ldflags of its
// own).
func TestVersionJSON_ReleaseBuildIncludesCommitAndGoVersion(t *testing.T) {
	old := outputFormat
	outputFormat = FormatJSON
	defer func() { outputFormat = old }()

	out := captureVersionJSON(t, "1.2.3", "abc1234")

	var env struct {
		Data struct {
			Version   string `json:"version"`
			Commit    string `json:"commit"`
			GoVersion string `json:"goVersion"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("expected valid JSON, got %q: %v", out, err)
	}
	if env.Data.Version != "1.2.3" {
		t.Errorf("expected version=1.2.3, got %q", env.Data.Version)
	}
	if env.Data.Commit != "abc1234" {
		t.Errorf("expected commit=abc1234, got %q", env.Data.Commit)
	}
	if env.Data.GoVersion == "" {
		t.Error("expected a non-empty goVersion for a non-dev build")
	}
}

// TestVersionInteractive_PrintsPlainVersionString confirms the
// unchanged, pre-existing default behavior: no --format=json means
// just the bare version string, exactly as before this feature
// existed.
func TestVersionInteractive_PrintsPlainVersionString(t *testing.T) {
	old := outputFormat
	outputFormat = FormatInteractive
	defer func() { outputFormat = old }()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stdout = w

	cmd := newVersionCmd("1.2.3", "abc1234")
	runErr := cmd.RunE(cmd, nil)

	w.Close()
	os.Stdout = oldStdout
	if runErr != nil {
		t.Fatalf("unexpected error: %v", runErr)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	if got := buf.String(); got != "1.2.3\n" {
		t.Errorf("expected plain %q, got %q", "1.2.3\n", got)
	}
}

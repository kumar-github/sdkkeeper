package cli

import (
	"encoding/json"
	"testing"
)

func runVersionCmd(t *testing.T, version, commit string) string {
	t.Helper()
	cmd := newVersionCmd(version, commit)
	return captureStdout(t, func() {
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Fatalf("newVersionCmd RunE returned an unexpected error: %v", err)
		}
	})
}

// TestVersionJSON confirms a dev build omits commit/goVersion (design
// doc §3), while a real, ldflags-injected build reports both.
func TestVersionJSON(t *testing.T) {
	old := outputFormat
	outputFormat = FormatJSON
	defer func() { outputFormat = old }()

	var env struct {
		Data struct {
			Version   string `json:"version"`
			Commit    string `json:"commit"`
			GoVersion string `json:"goVersion"`
		} `json:"data"`
	}

	if err := json.Unmarshal([]byte(runVersionCmd(t, "dev", "")), &env); err != nil {
		t.Fatalf("expected valid JSON: %v", err)
	}
	if env.Data.Version != "dev" || env.Data.Commit != "" || env.Data.GoVersion != "" {
		t.Errorf("expected a dev build to omit commit/goVersion, got: %+v", env.Data)
	}

	if err := json.Unmarshal([]byte(runVersionCmd(t, "1.2.3", "abc1234")), &env); err != nil {
		t.Fatalf("expected valid JSON: %v", err)
	}
	if env.Data.Version != "1.2.3" || env.Data.Commit != "abc1234" || env.Data.GoVersion == "" {
		t.Errorf("expected a release build to include commit/goVersion, got: %+v", env.Data)
	}
}

func TestVersionInteractive_PrintsPlainVersionString(t *testing.T) {
	old := outputFormat
	outputFormat = FormatInteractive
	defer func() { outputFormat = old }()

	if got := runVersionCmd(t, "1.2.3", "abc1234"); got != "1.2.3\n" {
		t.Errorf("expected plain %q, got %q", "1.2.3\n", got)
	}
}

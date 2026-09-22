package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sdkkeeper/internal/tooldef"
)

func TestRunDoctorFix_NothingToFix(t *testing.T) {
	setTestHome(t, t.TempDir())

	var runErr error
	out := withSession(t, func() {
		runErr = runDoctorFix()
	})
	if runErr != nil {
		t.Fatalf("expected success on a clean state, got: %v", runErr)
	}
	if !strings.Contains(out, "Nothing to fix") {
		t.Errorf("expected a 'nothing to fix' message, got: %q", out)
	}
}

// TestRunDoctorFix_DanglingRegistrationFullyFixed confirms the
// closed-loop guarantee: autofix genuinely removes the dangling
// symlink (checked by re-running checkDanglingRegistrations
// afterward, not just trusting the reported message), and since
// nothing is left to do by hand, no residual hint appears and the
// process succeeds.
func TestRunDoctorFix_DanglingRegistrationFullyFixed(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")

	realTarget := filepath.Join(t.TempDir(), "external-jdk")
	if err := os.MkdirAll(realTarget, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.MkdirAll(tool.CandidateRoot(), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	symlinkPath := filepath.Join(tool.CandidateRoot(), "JDK-99.0.0")
	if err := os.Symlink(realTarget, symlinkPath); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.RemoveAll(realTarget); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	var runErr error
	out := withSession(t, func() {
		runErr = runDoctorFix()
	})
	if runErr != nil {
		t.Fatalf("expected success -- fully auto-fixable, got: %v", runErr)
	}
	if !strings.Contains(out, "Fixed:") {
		t.Errorf("expected a 'Fixed:' line, got: %q", out)
	}
	if strings.Contains(out, "still needed") {
		t.Errorf("expected no residual hint for a fully-fixed issue, got: %q", out)
	}
	if _, err := os.Lstat(symlinkPath); !os.IsNotExist(err) {
		t.Error("expected the dangling symlink to actually be gone from disk")
	}
	if issues := checkDanglingRegistrations(); len(issues) != 0 {
		t.Errorf("expected re-running the check to find nothing left, got: %+v", issues)
	}
}

// TestRunDoctorFix_IncompleteInstallPartiallyFixed confirms the
// PARTIAL case: the broken directory is genuinely removed, but the
// process still reports needing attention (reinstalling is a manual
// step) and returns a non-nil error.
func TestRunDoctorFix_IncompleteInstallPartiallyFixed(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")
	target := filepath.Join(tool.CandidateRoot(), "JDK-21.0.2-temurin")
	if err := os.MkdirAll(tool.BinPath(target), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	var runErr error
	out := withSession(t, func() {
		runErr = runDoctorFix()
	})
	if runErr == nil {
		t.Fatal("expected a non-nil error -- reinstalling is still needed")
	}
	if !strings.Contains(out, "Fixed (partly)") {
		t.Errorf("expected a partial-fix line, got: %q", out)
	}
	if !strings.Contains(out, "sk install java 21.0.2-temurin") {
		t.Errorf("expected the residual reinstall hint, got: %q", out)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("expected the broken directory to actually be removed from disk")
	}
}

// TestRunDoctorFix_StaleDefaultPartiallyFixed confirms the default is
// genuinely cleared (readDefault afterward returns empty), with a
// residual hint to pick a new one.
func TestRunDoctorFix_StaleDefaultPartiallyFixed(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")
	if err := os.MkdirAll(filepath.Dir(tool.DefaultPath()), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.WriteFile(tool.DefaultPath(), []byte("99.0.0-temurin"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	var runErr error
	out := withSession(t, func() {
		runErr = runDoctorFix()
	})
	if runErr == nil {
		t.Fatal("expected a non-nil error -- setting a new default is still needed")
	}
	if !strings.Contains(out, "Fixed (partly)") {
		t.Errorf("expected a partial-fix line, got: %q", out)
	}
	if !strings.Contains(out, "sk default java <version>") {
		t.Errorf("expected the residual set-new-default hint, got: %q", out)
	}
	stored, err := readDefault(tool)
	if err != nil {
		t.Fatalf("readDefault failed: %v", err)
	}
	if stored != "" {
		t.Errorf("expected the stale default to actually be cleared, still reads %q", stored)
	}
}

func TestRunDoctorFix_LeftoverTempDirFullyFixed(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	leftover := filepath.Join(tooldef.TempRoot(), "extract-12345")
	if err := os.MkdirAll(leftover, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	var runErr error
	out := withSession(t, func() {
		runErr = runDoctorFix()
	})
	if runErr != nil {
		t.Fatalf("expected success -- fully auto-fixable, got: %v", runErr)
	}
	if !strings.Contains(out, "Fixed:") {
		t.Errorf("expected a 'Fixed:' line, got: %q", out)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Error("expected the leftover temp directory to actually be removed from disk")
	}
}

func TestBuildDoctorFixJSON_MixedOutcomesTallyCorrectly(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	// One fully-fixable (leftover temp dir) + one partially-fixable
	// (stale default) in the same run.
	leftover := filepath.Join(tooldef.TempRoot(), "extract-1")
	if err := os.MkdirAll(leftover, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	tool, _ := tooldef.Get("java")
	if err := os.MkdirAll(filepath.Dir(tool.DefaultPath()), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.WriteFile(tool.DefaultPath(), []byte("99.0.0"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	data := buildDoctorFixJSON()
	if data.Summary.Fixed != 2 {
		t.Errorf("expected 2 fixed (both autofix calls succeed), got %d", data.Summary.Fixed)
	}
	if data.Summary.NeedsAttention != 1 {
		t.Errorf("expected 1 needing attention (the stale default's residual), got %d", data.Summary.NeedsAttention)
	}
	if data.Summary.Failed != 0 {
		t.Errorf("expected 0 failed, got %d", data.Summary.Failed)
	}

	var staleDefaultResult *doctorFixResultJSON
	for i := range data.Results {
		if data.Results[i].Check == "stale_defaults" {
			staleDefaultResult = &data.Results[i]
		}
	}
	if staleDefaultResult == nil {
		t.Fatal("expected a stale_defaults result")
	}
	if !staleDefaultResult.Fixed || staleDefaultResult.Residual == "" {
		t.Errorf("expected Fixed=true with a non-empty Residual, got: %+v", staleDefaultResult)
	}
}

func TestBuildDoctorFixJSON_NothingToFixIsEmptyResults(t *testing.T) {
	setTestHome(t, t.TempDir())
	data := buildDoctorFixJSON()
	if len(data.Results) != 0 {
		t.Errorf("expected 0 results on a clean state, got: %+v", data.Results)
	}
	if data.Summary != (doctorFixSummaryJSON{}) {
		t.Errorf("expected an all-zero summary, got: %+v", data.Summary)
	}
}

func TestEmitDoctorFixJSON_NothingToFixReturnsNilError(t *testing.T) {
	setTestHome(t, t.TempDir())
	var runErr error
	out := captureStdout(t, func() {
		runErr = emitDoctorFixJSON(buildDoctorFixJSON())
	})
	if runErr != nil {
		t.Fatalf("expected nil error, got: %v", runErr)
	}
	if !strings.Contains(out, `"status":"ok"`) {
		t.Errorf("expected a success envelope, got: %q", out)
	}
}

func TestEmitDoctorFixJSON_NeedsAttentionReturnsCLIErrorButStillOkEnvelope(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")
	if err := os.MkdirAll(filepath.Dir(tool.DefaultPath()), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.WriteFile(tool.DefaultPath(), []byte("99.0.0"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	var runErr error
	out := captureStdout(t, func() {
		runErr = emitDoctorFixJSON(buildDoctorFixJSON())
	})
	if !strings.Contains(out, `"status":"ok"`) {
		t.Errorf("expected the envelope to still be status:ok, got: %q", out)
	}
	var cliErr *CLIError
	if !errors.As(runErr, &cliErr) {
		t.Fatalf("expected a *CLIError, got: %T (%v)", runErr, runErr)
	}
	if cliErr.Code != ErrCodeDoctorFixIncomplete {
		t.Errorf("expected doctor_fix_incomplete, got %q", cliErr.Code)
	}
	if ExitCode(runErr) != 201 {
		t.Errorf("expected exit code 201, got %d", ExitCode(runErr))
	}
}

// TestDoctorCmd_FixSubcommandIsWired confirms `sk doctor fix` is a
// real cobra subcommand (not a flag) reachable from the parent
// `doctor` command, and that it actually runs (not just parses).
func TestDoctorCmd_FixSubcommandIsWired(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	leftover := filepath.Join(tooldef.TempRoot(), "extract-1")
	if err := os.MkdirAll(leftover, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	doctorCmd := newDoctorCmd()
	found := false
	for _, sub := range doctorCmd.Commands() {
		if sub.Use == "fix" {
			found = true
			var runErr error
			withSession(t, func() {
				runErr = sub.RunE(sub, []string{})
			})
			if runErr != nil {
				t.Fatalf("expected success, got: %v", runErr)
			}
		}
	}
	if !found {
		t.Fatal("expected 'fix' to be registered as a subcommand of 'doctor'")
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Error("expected 'sk doctor fix' reached through the parent command to actually run")
	}
}

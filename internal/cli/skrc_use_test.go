package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sdkkeeper/internal/tooldef"
)

func writeSkrc(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, ".skrc")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	return path
}

// TestUse_ZeroArgsNoSkrcShowsBothExplanationAndUsageLine is the
// regression test for the exact wording fix: the message must
// explain BOTH ways to resolve it (name a tool, or add a .skrc) AND
// keep the original `usage: sk use ...` line that requireArgs used to
// print before the zero-arg case became legal at the Args level.
func TestUse_ZeroArgsNoSkrcShowsBothExplanationAndUsageLine(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	chdir(t, home) // no .skrc anywhere between here and $HOME

	var runErr error
	out := withSession(t, func() {
		cmd := newUseCmd()
		runErr = cmd.RunE(cmd, []string{})
	})

	if runErr == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(out, "sk use requires a tool name") || !strings.Contains(out, ".skrc file in this directory or a parent") {
		t.Errorf("expected the explanatory sentence, got: %q", out)
	}
	if !strings.Contains(out, "usage: sk use [<tool> [version|null]]") {
		t.Errorf("expected the original usage line to still be present, got: %q", out)
	}
}

func TestRunUseFromSkrc_AllInstalledActivatesEachOnItsOwnLine(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")
	installFakeMaven(t, home, "3.9.9")
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	writeSkrc(t, proj, "java=21.0.2-temurin\nmaven=3.9.9\n")
	chdir(t, proj)

	var runErr error
	out := withSession(t, func() {
		runErr = runUseFromSkrc()
	})

	if runErr != nil {
		t.Fatalf("expected success, got: %v", runErr)
	}
	if !strings.Contains(out, "JDK 21.0.2-temurin") || !strings.Contains(out, "(.skrc)") {
		t.Errorf("expected a per-candidate success line for java, got: %q", out)
	}
	if !strings.Contains(out, "Maven 3.9.9") {
		t.Errorf("expected a per-candidate success line for maven, got: %q", out)
	}
}

// TestRunUseFromSkrc_NotInstalledCandidateFailsButOthersStillApply
// confirms partial failure: one bad entry doesn't abort the batch,
// but the overall call still reports failure.
func TestRunUseFromSkrc_NotInstalledCandidateFailsButOthersStillApply(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	writeSkrc(t, proj, "java=21.0.2-temurin\nmaven=99.0.0\n")
	chdir(t, proj)

	var runErr error
	out := withSession(t, func() {
		runErr = runUseFromSkrc()
	})

	if runErr == nil {
		t.Fatal("expected an error since one candidate is not installed")
	}
	if !strings.Contains(out, "JDK 21.0.2-temurin") {
		t.Errorf("expected java to still succeed, got: %q", out)
	}
	if !strings.Contains(out, "not installed") {
		t.Errorf("expected a not-installed message for maven, got: %q", out)
	}
}

func TestRunUseFromSkrc_UnknownToolNameFails(t *testing.T) {
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
		runErr = runUseFromSkrc()
	})

	if runErr == nil {
		t.Fatal("expected an error for an unknown tool")
	}
	if !strings.Contains(out, "unknown tool") {
		t.Errorf("expected an unknown-tool message, got: %q", out)
	}
}

// TestRunUseFromSkrc_RequiresJavaWithoutJavaHomeOrEntryFails confirms
// the guard against resolveUse's own picker-chaining: a RequiresJava
// tool with neither a real JAVA_HOME nor a java entry in the same
// .skrc must fail cleanly, never launch an interactive picker
// mid-batch.
func TestRunUseFromSkrc_RequiresJavaWithoutJavaHomeOrEntryFails(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeMaven(t, home, "3.9.9")
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	writeSkrc(t, proj, "maven=3.9.9\n")
	chdir(t, proj)
	t.Setenv("JAVA_HOME", "") // explicitly unset for this process
	os.Unsetenv("JAVA_HOME")

	var runErr error
	out := withSession(t, func() {
		runErr = runUseFromSkrc()
	})

	if runErr == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(out, "needs a JDK") {
		t.Errorf("expected the JDK-prerequisite message, got: %q", out)
	}
}

// TestRunUseFromSkrc_JavaEntryMakesDependentToolVisibleInSameBatch is
// the main ordering test: java is applied first (regardless of file
// order) and its env var becomes visible in-process, so maven's own
// RequiresJava check passes within the same `sk use` invocation.
func TestRunUseFromSkrc_JavaEntryMakesDependentToolVisibleInSameBatch(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")
	installFakeMaven(t, home, "3.9.9")
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	// Deliberately maven BEFORE java in the file -- sorting inside
	// runUseFromSkrc must still process java first.
	writeSkrc(t, proj, "maven=3.9.9\njava=21.0.2-temurin\n")
	chdir(t, proj)
	os.Unsetenv("JAVA_HOME")

	var runErr error
	out := withSession(t, func() {
		runErr = runUseFromSkrc()
	})

	if runErr != nil {
		t.Fatalf("expected success, got: %v (%s)", runErr, out)
	}
	if !strings.Contains(out, "Maven 3.9.9") {
		t.Errorf("expected maven to activate once java made JAVA_HOME visible, got: %q", out)
	}
}

func TestRunUseFromSkrc_EmptySkrcIsNotAnError(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	writeSkrc(t, proj, "# No tools were active when this was generated — sk init skrc\n")
	chdir(t, proj)

	var runErr error
	out := withSession(t, func() {
		runErr = runUseFromSkrc()
	})

	if runErr != nil {
		t.Fatalf("expected no error for an empty .skrc, got: %v", runErr)
	}
	if !strings.Contains(out, "no candidates") {
		t.Errorf("expected a 'no candidates' message, got: %q", out)
	}
}

func TestSkrcOverride_DifferentPinReportsOverride(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	writeSkrc(t, proj, "java=21.0.2-temurin\n")
	chdir(t, proj)

	pinned, overridden := skrcOverride("java", "17.0.9-temurin")
	if !overridden {
		t.Error("expected overridden=true for a differing pin")
	}
	if pinned != "21.0.2-temurin" {
		t.Errorf("expected pinned=21.0.2-temurin, got %q", pinned)
	}
}

func TestSkrcOverride_MatchingPinReportsNoOverride(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	writeSkrc(t, proj, "java=21.0.2-temurin\n")
	chdir(t, proj)

	_, overridden := skrcOverride("java", "21.0.2-temurin")
	if overridden {
		t.Error("expected overridden=false when the activated version matches the pin")
	}
}

func TestSkrcOverride_NoSkrcReportsNoOverride(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	chdir(t, home)

	_, overridden := skrcOverride("java", "21.0.2-temurin")
	if overridden {
		t.Error("expected overridden=false when there is no .skrc at all")
	}
}

// TestUse_ExplicitVersionDifferingFromSkrcPinPrintsOverrideWarning is
// the end-to-end version of the override check: a real `sk use java
// <version>` call against a project whose .skrc pins something else
// must print the warning, worded as "Overriding project <Tool>
// <pinned> with <activated> for this shell".
func TestUse_ExplicitVersionDifferingFromSkrcPinPrintsOverrideWarning(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")
	installFakeJava(t, home, "17.0.9-temurin")
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	writeSkrc(t, proj, "java=21.0.2-temurin\n")
	chdir(t, proj)

	var runErr error
	out := withSession(t, func() {
		cmd := newUseCmd()
		runErr = cmd.RunE(cmd, []string{"java", "17.0.9-temurin"})
	})

	if runErr != nil {
		t.Fatalf("expected success, got: %v (%s)", runErr, out)
	}
	want := "Overriding project JDK 21.0.2-temurin with 17.0.9-temurin for this shell"
	if !strings.Contains(out, want) {
		t.Errorf("expected override warning %q, got: %q", want, out)
	}
}

// TestUse_ExplicitVersionMatchingSkrcPinPrintsNoWarning is the
// negative case, guarding against an overly-broad check that fires
// even when nothing was actually overridden.
func TestUse_ExplicitVersionMatchingSkrcPinPrintsNoWarning(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	writeSkrc(t, proj, "java=21.0.2-temurin\n")
	chdir(t, proj)

	var runErr error
	out := withSession(t, func() {
		cmd := newUseCmd()
		runErr = cmd.RunE(cmd, []string{"java", "21.0.2-temurin"})
	})

	if runErr != nil {
		t.Fatalf("expected success, got: %v (%s)", runErr, out)
	}
	if strings.Contains(out, "Overriding project") {
		t.Errorf("expected no override warning when the pin matches, got: %q", out)
	}
}

// TestUse_ZeroArgsJSONMatchesEmitSkrcUseJSON confirms the cobra wiring
// calls emitSkrcUseJSON (not the old not_implemented stopgap) and
// that a real batch actually activates -- envelope status "ok",
// per-candidate results, summary counts, and JAVA_HOME genuinely
// visible afterward via the os.Setenv trick.
func TestUse_ZeroArgsJSONMatchesEmitSkrcUseJSON(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	writeSkrc(t, proj, "java=21.0.2-temurin\n")
	chdir(t, proj)

	oldFormat := outputFormat
	outputFormat = FormatJSON
	defer func() { outputFormat = oldFormat }()

	var runErr error
	out := captureStdout(t, func() {
		cmd := newUseCmd()
		runErr = cmd.RunE(cmd, []string{})
	})
	if runErr != nil {
		t.Fatalf("expected success, got: %v", runErr)
	}
	if !strings.Contains(out, `"status":"ok"`) {
		t.Errorf("expected a success envelope, got: %q", out)
	}
	if !strings.Contains(out, `"activated":1`) {
		t.Errorf("expected summary.activated=1, got: %q", out)
	}
	if os.Getenv("JAVA_HOME") == "" {
		t.Error("expected JAVA_HOME to be set in-process after a successful JSON batch apply")
	}
}

// TestUse_ZeroArgsJSONNoSkrcReportsSkrcNotFound confirms the dedicated
// error code for the JSON path's own version of "nothing to apply".
func TestUse_ZeroArgsJSONNoSkrcReportsSkrcNotFound(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	chdir(t, home)

	oldFormat := outputFormat
	outputFormat = FormatJSON
	defer func() { outputFormat = oldFormat }()

	var runErr error
	captureStdout(t, func() {
		cmd := newUseCmd()
		runErr = cmd.RunE(cmd, []string{})
	})

	var cliErr *CLIError
	if !errors.As(runErr, &cliErr) {
		t.Fatalf("expected a *CLIError, got: %T (%v)", runErr, runErr)
	}
	if cliErr.Code != ErrCodeSkrcNotFound {
		t.Errorf("expected skrc_not_found, got %q", cliErr.Code)
	}
	if ExitCode(runErr) != 111 {
		t.Errorf("expected exit code 111, got %d", ExitCode(runErr))
	}
}

// TestBuildSkrcUseJSON_PartialFailureStillSucceedsAsEnvelope confirms
// the doctor-style split: the envelope itself is status "ok" with a
// real failed-candidate entry, while the PROCESS exit code (checked
// via emitSkrcUseJSON, not buildSkrcUseJSON directly) is what actually
// signals the partial failure.
func TestBuildSkrcUseJSON_PartialFailureStillSucceedsAsEnvelope(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	writeSkrc(t, proj, "java=21.0.2-temurin\nmaven=99.0.0\n")
	chdir(t, proj)

	data, jerr := buildSkrcUseJSON()
	if jerr != nil {
		t.Fatalf("expected the envelope itself to succeed, got error: %+v", jerr)
	}
	if data.Summary.Activated != 1 || data.Summary.Failed != 1 {
		t.Errorf("expected 1 activated, 1 failed, got: %+v", data.Summary)
	}
	var mavenResult *skrcUseResult
	for i := range data.Results {
		if data.Results[i].Tool == "maven" {
			mavenResult = &data.Results[i]
		}
	}
	if mavenResult == nil || mavenResult.Success {
		t.Fatalf("expected maven's result to report Success=false, got: %+v", mavenResult)
	}
	if mavenResult.Error == nil || mavenResult.Error.Code != ErrCodeNotFound {
		t.Errorf("expected maven's error to be not_found, got: %+v", mavenResult.Error)
	}
}

func TestEmitSkrcUseJSON_PartialFailureReturnsCLIErrorWithRightExitCode(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	writeSkrc(t, proj, "java=21.0.2-temurin\nmaven=99.0.0\n")
	chdir(t, proj)

	var runErr error
	out := captureStdout(t, func() {
		runErr = emitSkrcUseJSON()
	})
	if !strings.Contains(out, `"status":"ok"`) {
		t.Errorf("expected the envelope to still be status ok despite the partial failure, got: %q", out)
	}
	var cliErr *CLIError
	if !errors.As(runErr, &cliErr) {
		t.Fatalf("expected a *CLIError from the process's own exit path, got: %T (%v)", runErr, runErr)
	}
	if cliErr.Code != ErrCodeSkrcBatchPartialFailure {
		t.Errorf("expected skrc_batch_partial_failure, got %q", cliErr.Code)
	}
	if ExitCode(runErr) != 112 {
		t.Errorf("expected exit code 112, got %d", ExitCode(runErr))
	}
}

// TestTooldefRegistryHasRequiresJavaTools is a sanity check on a test
// assumption several tests above rely on (maven/gradle being
// RequiresJava=true) -- fails loudly if that ever changes, rather
// than the real tests above failing confusingly.
func TestTooldefRegistryHasRequiresJavaTools(t *testing.T) {
	tool, ok := tooldef.Get("maven")
	if !ok || !tool.RequiresJava {
		t.Fatal("test assumption broken: maven is expected to be RequiresJava=true")
	}
}

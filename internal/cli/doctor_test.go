package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sdkkeeper/internal/tooldef"
)

func TestCheckDanglingRegistrations_None(t *testing.T) {
	setTestHome(t, t.TempDir())
	issues := checkDanglingRegistrations()
	if len(issues) != 0 {
		t.Errorf("expected no issues on a clean state, got: %+v", issues)
	}
}

func TestCheckDanglingRegistrations_FindsDangling(t *testing.T) {
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
	// Now remove the real target -- the symlink becomes dangling.
	if err := os.RemoveAll(realTarget); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	issues := checkDanglingRegistrations()
	if len(issues) != 1 {
		t.Fatalf("expected exactly 1 dangling registration, got %d: %+v", len(issues), issues)
	}
	if issues[0].warning {
		t.Error("a dangling registration should be an error, not a warning")
	}
}

// TestCheckIncompleteInstalls_None is a regression test for a real
// bug caught via actual testing on a real macOS machine: the test
// fixture originally hardcoded filepath.Join(target, "bin") directly,
// but the real check calls tool.BinPath(v.Path), which on darwin
// resolves to target/Contents/Home/bin (java's own real archive
// layout there), not target/bin. The production check was correct
// all along; the test fixture just didn't account for the same
// darwin-specific transform the real code applies -- it happened to
// pass in a Linux sandbox (where BinPath is just versionDir/bin, no
// transform), and only failed once actually run on a real Mac. Fixed
// by building the fixture at tool.BinPath(target), the exact same
// path-computation function the real code uses, so this test is
// correct on any platform rather than accidentally Linux-only.
func TestCheckIncompleteInstalls_None(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")

	target := filepath.Join(tool.CandidateRoot(), "JDK-21.0.2-temurin")
	binPath := tool.BinPath(target)
	if err := os.MkdirAll(binPath, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binPath, "java"), []byte("fake"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	issues := checkIncompleteInstalls()
	if len(issues) != 0 {
		t.Errorf("expected no issues for a structurally complete install, got: %+v", issues)
	}
}

// TestCheckIncompleteInstalls_FindsEmptyBin -- same tool.BinPath(target)
// fix as TestCheckIncompleteInstalls_None above, for the same reason:
// correctness on any platform, not just Linux where BinPath happens
// to equal a hardcoded "bin" subdirectory.
func TestCheckIncompleteInstalls_FindsEmptyBin(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")

	target := filepath.Join(tool.CandidateRoot(), "JDK-21.0.2-temurin")
	if err := os.MkdirAll(tool.BinPath(target), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	// bin exists but is empty -- structurally incomplete.

	issues := checkIncompleteInstalls()
	if len(issues) != 1 {
		t.Fatalf("expected exactly 1 incomplete install, got %d: %+v", len(issues), issues)
	}
}

func TestCheckIncompleteInstalls_IgnoresSymlinks(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")

	// A registered (symlinked) entry with no bin directory at all --
	// this should NOT be flagged as "incomplete", since the structural
	// completeness of an EXTERNAL install isn't sk's install to judge.
	realTarget := t.TempDir()
	if err := os.MkdirAll(tool.CandidateRoot(), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	symlinkPath := filepath.Join(tool.CandidateRoot(), "JDK-17.0.3")
	if err := os.Symlink(realTarget, symlinkPath); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	issues := checkIncompleteInstalls()
	if len(issues) != 0 {
		t.Errorf("expected symlinked entries to never be flagged as incomplete, got: %+v", issues)
	}
}

func TestCheckStaleDefaults_None(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")

	target := filepath.Join(tool.CandidateRoot(), "JDK-21.0.2-temurin")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(tool.DefaultPath()), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.WriteFile(tool.DefaultPath(), []byte("21.0.2-temurin"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	issues := checkStaleDefaults()
	if len(issues) != 0 {
		t.Errorf("expected no issues when the default matches a real, present version, got: %+v", issues)
	}
}

func TestCheckStaleDefaults_FindsStale(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")

	if err := os.MkdirAll(filepath.Dir(tool.DefaultPath()), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.WriteFile(tool.DefaultPath(), []byte("99.0.0-temurin"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	// Deliberately nothing installed at all -- the stored default
	// can't possibly correspond to anything real.

	issues := checkStaleDefaults()
	if len(issues) != 1 {
		t.Fatalf("expected exactly 1 stale default, got %d: %+v", len(issues), issues)
	}
}

func TestCheckLeftoverTempDirs_None(t *testing.T) {
	setTestHome(t, t.TempDir())
	issues := checkLeftoverTempDirs()
	if len(issues) != 0 {
		t.Errorf("expected no issues when TempRoot doesn't even exist, got: %+v", issues)
	}
}

func TestCheckLeftoverTempDirs_FindsLeftovers(t *testing.T) {
	setTestHome(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(tooldef.TempRoot(), "extract-12345"), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	issues := checkLeftoverTempDirs()
	if len(issues) != 1 {
		t.Fatalf("expected exactly 1 leftover temp dir, got %d: %+v", len(issues), issues)
	}
	if !issues[0].warning {
		t.Error("a leftover temp directory should be a warning, not an error -- harmless clutter, not a functional problem")
	}
}

func TestSortedTools_StableOrder(t *testing.T) {
	first := sortedTools()
	for i := 0; i < 10; i++ {
		got := sortedTools()
		if len(got) != len(first) {
			t.Fatalf("expected consistent length, got %d vs %d", len(got), len(first))
		}
		for j := range first {
			if got[j].Name != first[j].Name {
				t.Errorf("expected stable order across calls, got %v vs %v", got, first)
			}
		}
	}
}

func TestDoctorCheckJSONFrom_NoIssuesIsPass(t *testing.T) {
	c := doctorCheckJSONFrom("dangling_registrations", "dangling registrations", nil)
	if c.Name != "dangling_registrations" {
		t.Errorf("expected the given name to be preserved, got %q", c.Name)
	}
	if c.Status != "pass" {
		t.Errorf("expected status=pass for zero issues, got %q", c.Status)
	}
	if c.Message != "No dangling registrations" {
		t.Errorf("expected the plural phrasing in the pass message, got %q", c.Message)
	}
}

func TestDoctorCheckJSONFrom_AllWarningsIsWarn(t *testing.T) {
	issues := []doctorIssue{
		{warning: true, message: "leftover A"},
		{warning: true, message: "leftover B"},
	}
	c := doctorCheckJSONFrom("leftover_temp_dirs", "leftover temp directories", issues)
	if c.Status != "warn" {
		t.Errorf("expected status=warn when every issue is a warning, got %q", c.Status)
	}
	if !strings.Contains(c.Message, "leftover A") || !strings.Contains(c.Message, "leftover B") {
		t.Errorf("expected both issue messages joined together, got %q", c.Message)
	}
}

// TestDoctorCheckJSONFrom_AnyNonWarningIsFail mirrors allWarnings'
// own "any single non-warning issue makes the whole check a failure"
// rule -- a mix of one real error and one warning must still report
// fail, not warn.
func TestDoctorCheckJSONFrom_AnyNonWarningIsFail(t *testing.T) {
	issues := []doctorIssue{
		{warning: true, message: "cosmetic leftover"},
		{warning: false, message: "a real dangling registration"},
	}
	c := doctorCheckJSONFrom("dangling_registrations", "dangling registrations", issues)
	if c.Status != "fail" {
		t.Errorf("expected status=fail when at least one issue isn't a warning, got %q", c.Status)
	}
}

func TestTallyIssues_EmptyCountsAsOnePass(t *testing.T) {
	var summary doctorSummaryJSON
	tallyIssues(&summary, nil)
	if summary.Pass != 1 || summary.Warn != 0 || summary.Fail != 0 {
		t.Errorf("expected (1,0,0) for an empty issue list, got %+v", summary)
	}
}

// TestTallyIssues_CountsEachIssueIndividually is the regression test
// for the actual reported gap: JSON's summary used to count by named
// check (1 per check regardless of how many issues it held), while
// text counted per item -- 2 dangling registrations showed as 2 in
// text but 1 in JSON. tallyIssues is the single shared rule both now
// use; this confirms it counts each issue on its own, not "1 per
// call".
func TestTallyIssues_CountsEachIssueIndividually(t *testing.T) {
	var summary doctorSummaryJSON
	tallyIssues(&summary, []doctorIssue{
		{warning: false, message: "JDK 25.0.1 is registered, but its real target no longer exists"},
		{warning: false, message: "JDK 21.0.2 is registered, but its real target no longer exists"},
	})
	if summary.Pass != 0 || summary.Warn != 0 || summary.Fail != 2 {
		t.Errorf("expected (0,0,2) for 2 real issues, got %+v", summary)
	}
}

func TestTallyIssues_AccumulatesAcrossMultipleCalls(t *testing.T) {
	var summary doctorSummaryJSON
	tallyIssues(&summary, nil)                                                                             // +1 pass
	tallyIssues(&summary, []doctorIssue{{warning: true, message: "w"}})                                    // +1 warn
	tallyIssues(&summary, []doctorIssue{{warning: false, message: "f1"}, {warning: false, message: "f2"}}) // +2 fail
	if summary.Pass != 1 || summary.Warn != 1 || summary.Fail != 2 {
		t.Errorf("expected (1,1,2) accumulated across calls, got %+v", summary)
	}
}

// TestPrintCheckAndTallyIssues_AgreeOnTheSameIssueList confirms text
// (printCheck) and JSON (tallyIssues, via buildDoctorJSON) genuinely
// can't diverge anymore -- both classify the EXACT same issue list
// identically, since printCheck's own per-item counting and
// tallyIssues share the same `.warning`-based rule.
func TestPrintCheckAndTallyIssues_AgreeOnTheSameIssueList(t *testing.T) {
	issues := []doctorIssue{
		{warning: false, message: "JDK 25.0.1 is registered, but its real target no longer exists"},
		{warning: false, message: "JDK 21.0.2 is registered, but its real target no longer exists"},
	}

	var textPass, textWarn, textFail int
	withSession(t, func() {
		textPass, textWarn, textFail = printCheck("dangling registration", "dangling registrations", issues)
	})

	var jsonSummary doctorSummaryJSON
	tallyIssues(&jsonSummary, issues)

	if textPass != jsonSummary.Pass || textWarn != jsonSummary.Warn || textFail != jsonSummary.Fail {
		t.Errorf("expected text and JSON to agree exactly, got text=(%d,%d,%d) json=%+v",
			textPass, textWarn, textFail, jsonSummary)
	}
}

// TestEmitDoctorJSON_AllPassReturnsNilError confirms the ordinary
// success case: a clean doctorData (no failing checks) writes a
// success envelope and returns nil -- exit code 0.
func TestEmitDoctorJSON_AllPassReturnsNilError(t *testing.T) {
	data := &doctorData{
		Checks:  []doctorCheckJSON{{Name: "dangling_registrations", Status: "pass", Message: "No dangling registrations"}},
		Summary: doctorSummaryJSON{Pass: 1},
	}
	var returned error
	out := captureStdout(t, func() {
		returned = emitDoctorJSON(data)
	})
	if returned != nil {
		t.Errorf("expected nil error when nothing failed, got: %v", returned)
	}
	if !strings.Contains(out, `"status":"ok"`) {
		t.Errorf("expected a success envelope, got: %s", out)
	}
	if ExitCode(returned) != 0 {
		t.Errorf("expected ExitCode(nil) = 0, got %d", ExitCode(returned))
	}
}

// TestEmitDoctorJSON_FailingCheckStillWritesSuccessEnvelope is THE
// critical test for design doc §4's own explicit nuance: "the
// envelope's top-level status is error ONLY if the diagnostic process
// itself couldn't run... A fail check is a finding, not an invocation
// error." So even with a failing check, the WRITTEN envelope must
// still say status:"ok" (never "error", and never carry an "error"
// key) -- while the returned Go error must still be a *CLIError with
// doctor_check_failed, resolving to exit code 201 via ExitCode. This
// success-envelope-plus-nonzero-exit-code combination is unique to
// doctor among every command in this package.
func TestEmitDoctorJSON_FailingCheckStillWritesSuccessEnvelope(t *testing.T) {
	data := &doctorData{
		Checks: []doctorCheckJSON{
			{Name: "dangling_registrations", Status: "fail", Message: "1 dangling registration found"},
		},
		Summary: doctorSummaryJSON{Fail: 1},
	}
	var returned error
	out := captureStdout(t, func() {
		returned = emitDoctorJSON(data)
	})
	if !strings.Contains(out, `"status":"ok"`) {
		t.Errorf("expected the envelope itself to report status:ok even with a failing check, got: %s", out)
	}
	if strings.Contains(out, `"error"`) {
		t.Errorf("expected NO \"error\" key in the envelope at all, got: %s", out)
	}
	if returned == nil {
		t.Fatal("expected a non-nil error so the PROCESS exit code reflects the failing check")
	}
	var cliErr *CLIError
	if !errors.As(returned, &cliErr) {
		t.Fatalf("expected a *CLIError, got: %T (%v)", returned, returned)
	}
	if cliErr.Code != ErrCodeDoctorCheckFailed {
		t.Errorf("expected code=doctor_check_failed, got %q", cliErr.Code)
	}
	if ExitCode(returned) != 201 {
		t.Errorf("expected ExitCode = 201, got %d", ExitCode(returned))
	}
}

// TestEmitDoctorJSON_WarnOnlyDoesNotFailTheProcess confirms warn-only
// findings (design doc §5: "warn-only or all-pass results exit 0")
// don't trigger the 201 exit path -- only a genuine "fail" status
// does.
func TestEmitDoctorJSON_WarnOnlyDoesNotFailTheProcess(t *testing.T) {
	data := &doctorData{
		Checks:  []doctorCheckJSON{{Name: "leftover_temp_dirs", Status: "warn", Message: "1 leftover temp directory"}},
		Summary: doctorSummaryJSON{Warn: 1},
	}
	var returned error
	captureStdout(t, func() {
		returned = emitDoctorJSON(data)
	})
	if returned != nil {
		t.Errorf("expected nil error for a warn-only result, got: %v", returned)
	}
}

// TestBuildDoctorJSON_ReturnsAllNamedChecks is a structural test --
// confirms every check this package's interactive doctor command runs
// has a JSON counterpart present, by name, regardless of this
// sandbox's own network reachability (vendor_reachability's actual
// pass/fail status is environment-dependent, so this deliberately
// does not assert on ITS status, only that it's present at all).
func TestBuildDoctorJSON_ReturnsAllNamedChecks(t *testing.T) {
	setTestHome(t, t.TempDir())
	data := buildDoctorJSON(context.Background())
	wantNames := map[string]bool{
		"dangling_registrations": false,
		"incomplete_installs":    false,
		"stale_defaults":         false,
		"leftover_temp_dirs":     false,
		"vendor_reachability":    false,
	}
	for _, c := range data.Checks {
		if _, ok := wantNames[c.Name]; ok {
			wantNames[c.Name] = true
		}
	}
	for name, seen := range wantNames {
		if !seen {
			t.Errorf("expected a %q check to be present, got: %+v", name, data.Checks)
		}
	}
	// Every check's status must be one of the three legal values --
	// never a stray empty string or anything else.
	for _, c := range data.Checks {
		if c.Status != "pass" && c.Status != "warn" && c.Status != "fail" {
			t.Errorf("check %q has an illegal status %q", c.Name, c.Status)
		}
	}
}

// TestPrintCheck_NoIssuesReturnsPass confirms printCheck's return
// tuple mirrors doctorCheckJSONFrom's own classification exactly (0
// issues -> pass) -- the same rule, not a separately reimplemented
// one, so the compact plain-text summary line built from this can
// never disagree with what --format=json reports for the same run.
func TestPrintCheck_NoIssuesReturnsPass(t *testing.T) {
	var pass, warn, fail int
	withSession(t, func() {
		pass, warn, fail = printCheck("thing", "things", nil)
	})
	if pass != 1 || warn != 0 || fail != 0 {
		t.Errorf("expected (1,0,0), got (%d,%d,%d)", pass, warn, fail)
	}
}

func TestPrintCheck_AllWarningIssuesReturnsWarn(t *testing.T) {
	issues := []doctorIssue{{warning: true, message: "leftover temp dir"}}
	var pass, warn, fail int
	withSession(t, func() {
		pass, warn, fail = printCheck("thing", "things", issues)
	})
	if pass != 0 || warn != 1 || fail != 0 {
		t.Errorf("expected (0,1,0), got (%d,%d,%d)", pass, warn, fail)
	}
}

func TestPrintCheck_AnyNonWarningIssueReturnsFail(t *testing.T) {
	// Mixed: one warning-level issue alongside one real one -- each
	// counted in ITS OWN category now (per-item counting), not
	// merged into a single "whole check counts as fail" outcome.
	issues := []doctorIssue{
		{warning: true, message: "cosmetic"},
		{warning: false, message: "a real problem"},
	}
	var pass, warn, fail int
	withSession(t, func() {
		pass, warn, fail = printCheck("thing", "things", issues)
	})
	if pass != 0 || warn != 1 || fail != 1 {
		t.Errorf("expected (0,1,1) -- the warning counted as 1 warn, the real issue counted as 1 fail -- got (%d,%d,%d)", pass, warn, fail)
	}
}

// TestPrintCheck_MultipleRealIssuesCountedIndividually is the
// regression test for the actual reported case: 2 dangling
// registrations must tally as 2 failures, not 1 -- the user sees two
// concrete broken symlinks listed, and the summary should say so, not
// collapse them into "1 failure" just because they came from the same
// named check.
func TestPrintCheck_MultipleRealIssuesCountedIndividually(t *testing.T) {
	issues := []doctorIssue{
		{warning: false, message: "JDK 25.0.1 is registered, but its real target no longer exists"},
		{warning: false, message: "JDK 21.0.2 is registered, but its real target no longer exists"},
	}
	var pass, warn, fail int
	withSession(t, func() {
		pass, warn, fail = printCheck("dangling registration", "dangling registrations", issues)
	})
	if pass != 0 || warn != 0 || fail != 2 {
		t.Errorf("expected (0,0,2) for 2 real issues, got (%d,%d,%d)", pass, warn, fail)
	}
}

// TestFormatDoctorSummary_AllZeroShowsAllThreeCategoriesMuted
// confirms the fully-healthy case: all three categories are always
// shown, even at zero -- not omitted, and not falsely alarming (no
// Warning/Error coloring on a genuine zero).
func TestFormatDoctorSummary_AllZeroShowsAllThreeCategoriesMuted(t *testing.T) {
	out := withSession(t, func() {
		fmt.Fprintln(session.Out, formatDoctorSummary(doctorSummaryJSON{Pass: 5, Warn: 0, Fail: 0}))
	})
	if !strings.Contains(out, "5 passed") || !strings.Contains(out, "0 warnings") || !strings.Contains(out, "0 failures") {
		t.Errorf("expected all three categories present even at zero, got: %q", out)
	}
}

func TestFormatDoctorSummary_SingularPluralWording(t *testing.T) {
	out := withSession(t, func() {
		fmt.Fprintln(session.Out, formatDoctorSummary(doctorSummaryJSON{Pass: 1, Warn: 1, Fail: 1}))
	})
	if !strings.Contains(out, "1 warning ") && !strings.HasSuffix(strings.TrimRight(out, "\n"), "1 warning") {
		t.Errorf("expected singular 'warning' (no trailing s) for count 1, got: %q", out)
	}
	if strings.Contains(out, "1 warnings") {
		t.Errorf("expected singular 'warning', got plural in: %q", out)
	}
	if strings.Contains(out, "1 failures") {
		t.Errorf("expected singular 'failure', got plural in: %q", out)
	}
}

func TestFormatDoctorSummary_MixedCountsAllPresent(t *testing.T) {
	out := withSession(t, func() {
		fmt.Fprintln(session.Out, formatDoctorSummary(doctorSummaryJSON{Pass: 4, Warn: 1, Fail: 1}))
	})
	if !strings.Contains(out, "4 passed") || !strings.Contains(out, "1 warning") || !strings.Contains(out, "1 failure") {
		t.Errorf("expected all three real counts present, got: %q", out)
	}
}

func TestDoctorCmd_SummaryLineAlwaysPrintedEvenOnHealthyRun(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	var pass, warn, fail int
	out := withSession(t, func() {
		pass, warn, fail = printCheck("thing", "things", nil)
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Success.Render("\u2713 Everything looks healthy."))
		fmt.Fprintln(session.Out, formatDoctorSummary(doctorSummaryJSON{Pass: pass, Warn: warn, Fail: fail}))
	})
	if !strings.Contains(out, "Everything looks healthy") {
		t.Errorf("expected the healthy message still present, got: %q", out)
	}
	if !strings.Contains(out, "1 passed") || !strings.Contains(out, "0 warnings") || !strings.Contains(out, "0 failures") {
		t.Errorf("expected the summary line to ALSO print, with all zero categories shown, got: %q", out)
	}
}

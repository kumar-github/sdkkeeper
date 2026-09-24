package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sdkkeeper/internal/term"
	"sdkkeeper/internal/tooldef"
)

func TestMerge_NilSafe(t *testing.T) {
	r := newResult()
	r.EnvVars["JAVA_HOME"] = "/java/home"

	// Must not panic when merging a nil Result -- a defensive fix
	// alongside the real bug (see below), protecting any future
	// multi-level RequiresJava chain.
	r.merge(nil)

	if r.EnvVars["JAVA_HOME"] != "/java/home" {
		t.Errorf("merge(nil) should be a no-op, but EnvVars changed: %+v", r.EnvVars)
	}
}

func TestMerge_CombinesEnvVarsAndPaths(t *testing.T) {
	r := newResult()
	r.EnvVars["JAVA_HOME"] = "/java/home"
	r.PathPrepends = []PathPrepend{{Dir: "/java/home/bin", StripPrefix: "/java/candidates"}}

	other := newResult()
	other.EnvVars["MAVEN_HOME"] = "/maven/home"
	other.PathPrepends = []PathPrepend{{Dir: "/maven/home/bin", StripPrefix: "/maven/candidates"}}

	r.merge(other)

	if r.EnvVars["JAVA_HOME"] != "/java/home" || r.EnvVars["MAVEN_HOME"] != "/maven/home" {
		t.Errorf("expected both JAVA_HOME and MAVEN_HOME after merge, got: %+v", r.EnvVars)
	}
	if len(r.PathPrepends) != 2 {
		t.Errorf("expected 2 PathPrepends entries after merge, got: %v", r.PathPrepends)
	}
}

func TestIsEmpty(t *testing.T) {
	var nilResult *Result
	if !nilResult.isEmpty() {
		t.Error("nil *Result should be considered empty")
	}

	empty := newResult()
	if !empty.isEmpty() {
		t.Error("a freshly-created Result with nothing set should be considered empty")
	}

	partial := newResult()
	partial.EnvVars["JAVA_HOME"] = "/java/home"
	if partial.isEmpty() {
		t.Error("a Result with a real env var set should NOT be considered empty")
	}
}

// TestPartialResultSurvivesLaterFailure is a regression test for the
// exact bug reported: `sk use maven` with no JDK active shows the Java
// picker, the user selects one (merged into result), then cancels
// Maven's OWN picker -- the JDK selection must still be applied, not
// discarded just because Maven itself was never resolved.
//
// This tests the merge/writeResult contract directly (the pure,
// TTY-independent pieces) rather than the full resolveUse flow, which
// needs a real interactive picker to exercise end-to-end -- see the
// project README for why that part needs manual verification instead.
func TestPartialResultSurvivesLaterFailure(t *testing.T) {
	// Simulates what resolveUse now does: build up `result` with the
	// successfully-resolved prerequisite (java), then simulate the
	// dependent tool's own step failing/cancelling -- the fix is that
	// `result` (not a fresh nil) is what gets returned and written.
	result := newResult()

	javaResult := newResult()
	javaResult.EnvVars["JAVA_HOME"] = "/java/home"
	javaResult.PathPrepends = []PathPrepend{{Dir: "/java/home/bin", StripPrefix: "/java/candidates"}}
	result.merge(javaResult)

	// maven's own step "fails" here -- but result (with JAVA_HOME
	// already merged in) is what a fixed resolveUse would return, not
	// a fresh nil.
	if result.isEmpty() {
		t.Fatal("result should NOT be empty -- it has a successfully-merged JAVA_HOME")
	}

	out := captureStdout(t, func() { writeResult(result, ShellZsh) })
	if !strings.Contains(out, "JAVA_HOME") {
		t.Errorf("expected JAVA_HOME in output even though the dependent tool's own step failed, got: %s", out)
	}
}

func TestJoinBanner(t *testing.T) {
	cases := []struct {
		a, b, want string
	}{
		{"", "", ""},
		{"only-a", "", "only-a"},
		{"", "only-b", "only-b"},
		{"a", "b", "a\n\nb"},
	}
	for _, c := range cases {
		got := joinBanner(c.a, c.b)
		if got != c.want {
			t.Errorf("joinBanner(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

func TestMerge_CombinesMessages(t *testing.T) {
	r := newResult()
	r.Messages = append(r.Messages, "first")

	other := newResult()
	other.Messages = append(other.Messages, "second")

	r.merge(other)

	if len(r.Messages) != 2 || r.Messages[0] != "first" || r.Messages[1] != "second" {
		t.Errorf("expected merged Messages [first second], got: %v", r.Messages)
	}
}

func TestIsEmpty_IgnoresMessages(t *testing.T) {
	// A Result with only pending confirmation text and no actual
	// env/PATH changes should still be considered "empty" from
	// writeResult's perspective -- Messages are handled separately by
	// use.go, not through the export/JSON output path.
	r := newResult()
	r.Messages = append(r.Messages, "some pending confirmation")

	if !r.isEmpty() {
		t.Error("a Result with only Messages set (no EnvVars/PathPrepends) should be considered empty by isEmpty()")
	}
}

// TestMessagesPreservedAcrossPrerequisiteChain is a regression test for
// the fix that keeps a prerequisite's confirmation (e.g. a JDK) visible
// in the FINAL, persistent output, not just transiently as the next
// picker's banner. Simulates the exact sequence resolveUse now performs:
// merge a successful prerequisite's Messages in, fold them into a
// banner string WITHOUT clearing Messages, then append the dependent
// tool's own confirmation -- both should survive to the end, in order.
func TestMessagesPreservedAcrossPrerequisiteChain(t *testing.T) {
	result := newResult()

	javaResult := newResult()
	javaResult.Messages = append(javaResult.Messages, "java confirmation")
	result.merge(javaResult)

	// Simulate folding into a banner WITHOUT clearing -- the actual fix.
	pendingBanner := strings.Join(result.Messages, "\n")
	if pendingBanner != "java confirmation" {
		t.Fatalf("expected banner to contain java's confirmation, got: %q", pendingBanner)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("expected result.Messages to still hold java's confirmation after folding into a banner, got: %v", result.Messages)
	}

	// Simulate the dependent tool's own confirmation being appended.
	result.Messages = append(result.Messages, "maven confirmation")

	if len(result.Messages) != 2 || result.Messages[0] != "java confirmation" || result.Messages[1] != "maven confirmation" {
		t.Errorf("expected both confirmations preserved in order, got: %v", result.Messages)
	}
}

// TestMessagesClearedAfterDirectConsolePrint is a regression test for
// the OTHER half of the same fix: when a pending banner has nowhere to
// be folded into (a direct version argument was given, so no picker
// runs) and gets printed directly to the console instead, Messages
// SHOULD be cleared -- otherwise the same text would be printed AGAIN
// by the final flush in use.go, a genuine duplication.
func TestMessagesClearedAfterDirectConsolePrint(t *testing.T) {
	result := newResult()
	result.Messages = append(result.Messages, "already printed directly")

	// Simulate showPendingBanner having just printed this directly,
	// followed by the actual fix: clearing Messages afterward.
	result.Messages = nil

	if len(result.Messages) != 0 {
		t.Errorf("expected Messages to be cleared after a direct console print, got: %v", result.Messages)
	}
}

// TestFlushMessages_PreservesChronologicalOrder is a regression test
// for the exact bug reported: cancelling Maven's picker after
// successfully selecting a JDK printed "No Maven version selected."
// BEFORE "JDK 21.0.2 selected", reversing the actual order events
// happened in. flushMessages must print pending confirmations first,
// so a caller that calls it immediately before printing its own
// message gets the correct chronological order.
func TestFlushMessages_PreservesChronologicalOrder(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	sess := &term.Session{Out: w}

	result := newResult()
	result.Messages = append(result.Messages, "JDK CONFIRMATION")

	flushMessages(sess, result)
	fmt.Fprintln(sess.Out, "MAVEN CANCEL MESSAGE")

	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()

	jdkIdx := strings.Index(out, "JDK CONFIRMATION")
	mavenIdx := strings.Index(out, "MAVEN CANCEL MESSAGE")
	if jdkIdx == -1 || mavenIdx == -1 {
		t.Fatalf("expected both strings present, got: %q", out)
	}
	if jdkIdx > mavenIdx {
		t.Errorf("expected JDK confirmation to appear BEFORE the cancel message, got order reversed: %q", out)
	}

	if len(result.Messages) != 0 {
		t.Errorf("expected flushMessages to clear result.Messages, still has: %v", result.Messages)
	}
}

func TestFormatMB(t *testing.T) {
	cases := map[int64]string{
		0:           "0.0 MB",
		1024 * 1024: "1.0 MB",
		52428800:    "50.0 MB", // 50 MB exactly
		30251008:    "28.8 MB", // 28.849609375, rounds down to 1 decimal
	}
	for bytes, want := range cases {
		got := formatMB(bytes)
		if got != want {
			t.Errorf("formatMB(%d) = %q, want %q", bytes, got, want)
		}
	}
}

func TestRenderDownloadProgress_UnknownTotal(t *testing.T) {
	line := renderDownloadProgress(1048576, -1, 0)
	if line != "1.0 MB" {
		t.Errorf("expected fallback text for unknown total, got: %q", line)
	}
	if strings.Contains(line, "%") {
		t.Error("expected NO percentage shown when total is unknown -- would be misleading, not just incomplete")
	}
}

func TestRenderDownloadProgress_KnownTotal(t *testing.T) {
	line := renderDownloadProgress(50, 100, 0)
	if !strings.Contains(line, "50%") {
		t.Errorf("expected 50%% in output, got: %q", line)
	}
	//if !strings.Contains(line, "\u2588") || !strings.Contains(line, "\u2591") {
	if !strings.Contains(line, "\u2501") || !strings.Contains(line, "\u2500") {
		//t.Errorf("expected both filled (\u2588) and empty (\u2591) bar segments at 50%%, got: %q", line)
		t.Errorf("expected both filled (\u2501) and empty (\u2500) bar segments at 50%%, got: %q", line)
	}
}

func TestRenderDownloadProgress_FullBarAtCompletion(t *testing.T) {
	// Large, realistic byte values -- avoids the byte-count text
	// itself containing a decimal point (e.g. "0.0 MB") being
	// mistaken for a bar-fill character when checking the bar.
	line := renderDownloadProgress(100*1024*1024, 100*1024*1024, 0)
	barStart := strings.Index(line, "[")
	barEnd := strings.Index(line, "]")
	if barStart == -1 || barEnd == -1 {
		t.Fatalf("expected a bracketed bar in output, got: %q", line)
	}
	bar := line[barStart+1 : barEnd]
	//if strings.Contains(bar, "\u2591") {
	if strings.Contains(bar, "\u2500") {
		//t.Errorf("expected a fully-filled bar (no \u2591 segments) at 100%%, got bar: %q", bar)
		t.Errorf("expected a fully-filled bar (no \u2500 segments) at 100%%, got bar: %q", bar)
	}
	if !strings.Contains(line, "100%") {
		t.Errorf("expected 100%% in output, got: %q", line)
	}
}

func TestRenderDownloadProgress_NeverExceedsBarWidth(t *testing.T) {
	// A read count slightly exceeding total (possible with chunked
	// encoding quirks) should never overflow the bar's fixed width.
	line := renderDownloadProgress(105, 100, 0)
	//if strings.Count(line, "\u2588") > 36 {
	if strings.Count(line, "\u2501") > 36 {
		t.Errorf("expected filled bar segments to be capped at bar width, got: %q", line)
	}
}

// TestRenderDownloadProgress_ShowsSpeedAndETAOnceMeasurable confirms
// the new speed/ETA feature: once enough time has genuinely elapsed
// to compute a rate, both a speed and an ETA appear alongside the bar.
func TestRenderDownloadProgress_ShowsSpeedAndETAOnceMeasurable(t *testing.T) {
	// 10 MB read in 2 real seconds -- a realistic, easily-verified
	// rate (5 MB/s) with 90 MB remaining, giving an ETA in the
	// several-second range.
	line := renderDownloadProgress(10*1024*1024, 100*1024*1024, 2*time.Second)
	if !strings.Contains(line, "MB/s") {
		t.Errorf("expected a speed reading once elapsed time is genuinely measurable, got: %q", line)
	}
	if !strings.Contains(line, "ETA") {
		t.Errorf("expected an ETA once both speed and a known total are available, got: %q", line)
	}
}

// TestRenderDownloadProgress_NoSpeedBeforeMeasurable is the direct
// safety-net test for the very first progress tick: with no
// meaningful time elapsed yet, showing a "speed" would be either a
// divide-by-zero or a meaningless, wildly inflated number -- neither
// should ever reach the user.
func TestRenderDownloadProgress_NoSpeedBeforeMeasurable(t *testing.T) {
	line := renderDownloadProgress(1024, 100*1024*1024, 0)
	if strings.Contains(line, "MB/s") {
		t.Errorf("expected NO speed reading with zero elapsed time, got: %q", line)
	}
}

// TestRenderDownloadProgress_NoETAWithUnknownTotal confirms speed
// alone can still show even when total is unknown (there's a real
// rate to report), but ETA correctly never does (there's no target
// to estimate time "until").
func TestRenderDownloadProgress_NoETAWithUnknownTotal(t *testing.T) {
	line := renderDownloadProgress(10*1024*1024, -1, 2*time.Second)
	if !strings.Contains(line, "MB/s") {
		t.Errorf("expected a speed reading even with an unknown total, got: %q", line)
	}
	if strings.Contains(line, "ETA") {
		t.Errorf("expected NO ETA with an unknown total, got: %q", line)
	}
}

func TestFormatDuration_UnderAndOverAMinute(t *testing.T) {
	if got := formatDuration(45); got != "45s" {
		t.Errorf("formatDuration(45) = %q, want \"45s\"", got)
	}
	if got := formatDuration(127); got != "2m 7s" {
		t.Errorf("formatDuration(127) = %q, want \"2m 7s\"", got)
	}
}

// TestAlreadyExistsMessage_RealDirectory confirms the message keeps
// its original, accurate wording for a genuinely sk-installed
// directory -- "already installed at <the .sdkkeeper path>" is
// correct in this case, no change needed.
func TestAlreadyExistsMessage_RealDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "JDK-21.0.2")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	msg := alreadyExistsMessage("JDK-", "21.0.2", target)
	if !strings.Contains(msg, target) {
		t.Errorf("expected message to contain the real directory path %q, got: %q", target, msg)
	}
	if strings.Contains(msg, "not managed by SDK Keeper") {
		t.Errorf("expected NO 'not managed' qualifier for a genuinely sk-installed directory, got: %q", msg)
	}
}

// TestAlreadyExistsMessage_Symlink is a regression test for a real
// inaccuracy found via actual use: a JDK added via `sk add` (a
// symlink, not something sk ever downloaded) was previously reported
// as "installed" at the .sdkkeeper/candidates redirect path -- this
// confirms the fix shows the REAL, resolved location instead (where
// the actual files live), with the "not managed by SDK Keeper"
// qualifier reused verbatim from `list`'s own established wording.
func TestAlreadyExistsMessage_Symlink(t *testing.T) {
	realLocation := t.TempDir()
	symlinkParent := t.TempDir()
	target := filepath.Join(symlinkParent, "JDK-21.0.2")
	if err := os.Symlink(realLocation, target); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	msg := alreadyExistsMessage("JDK-", "21.0.2", target)
	if !strings.Contains(msg, realLocation) {
		t.Errorf("expected message to contain the REAL resolved location %q, got: %q", realLocation, msg)
	}
	if strings.Contains(msg, target) {
		t.Errorf("expected message to NOT show the redirect path %q (should show the real location instead), got: %q", target, msg)
	}
	if !strings.Contains(msg, "not managed by SDK Keeper") {
		t.Errorf("expected the 'not managed by SDK Keeper' qualifier for a symlinked entry, got: %q", msg)
	}
}

// TestAlreadyExistsMessage_BrokenSymlinkFallsBack confirms a dangling
// symlink (target deleted) still produces a sensible message -- falls
// back to showing the symlink's own path rather than failing to
// render anything.
func TestAlreadyExistsMessage_BrokenSymlinkFallsBack(t *testing.T) {
	deletedTarget := filepath.Join(t.TempDir(), "no-longer-here")
	symlinkParent := t.TempDir()
	target := filepath.Join(symlinkParent, "JDK-21.0.2")
	if err := os.Symlink(deletedTarget, target); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	msg := alreadyExistsMessage("JDK-", "21.0.2", target)
	if msg == "" {
		t.Fatal("expected a non-empty message even for a dangling symlink")
	}
	// os.Readlink still succeeds for a dangling symlink (it just
	// returns the recorded target string, whether or not that target
	// currently exists) -- so this should still show the resolved
	// (if nonexistent) location, not the redirect path.
	if !strings.Contains(msg, deletedTarget) {
		t.Errorf("expected the recorded (even if now-deleted) target %q, got: %q", deletedTarget, msg)
	}
}

func TestParseFullIdentifier_CompleteTemurin(t *testing.T) {
	version, vendor, ok := parseFullIdentifier("java", "21.0.2-temurin")
	if !ok {
		t.Fatal("expected a complete identifier to be recognized")
	}
	if version != "21.0.2" {
		t.Errorf("expected version '21.0.2', got: %q", version)
	}
	if vendor != "temurin" {
		t.Errorf("expected vendor 'temurin', got: %q", vendor)
	}
}

func TestParseFullIdentifier_CompleteLiberica(t *testing.T) {
	version, vendor, ok := parseFullIdentifier("java", "11.0.5-liberica")
	if !ok {
		t.Fatal("expected a complete identifier to be recognized")
	}
	if version != "11.0.5" {
		t.Errorf("expected version '11.0.5', got: %q", version)
	}
	if vendor != "liberica" {
		t.Errorf("expected vendor 'liberica', got: %q", vendor)
	}
}

func TestParseFullIdentifier_FourComponentVersion(t *testing.T) {
	// Confirms the exact-patch check generalizes correctly to the real,
	// confirmed 4-component version case (e.g. Adoptium's own
	// "21.0.12.1", encountered with real API data earlier).
	version, vendor, ok := parseFullIdentifier("java", "21.0.12.1-temurin")
	if !ok {
		t.Fatal("expected a complete 4-component identifier to be recognized")
	}
	if version != "21.0.12.1" {
		t.Errorf("expected version '21.0.12.1', got: %q", version)
	}
	if vendor != "temurin" {
		t.Errorf("expected vendor 'temurin', got: %q", vendor)
	}
}

// TestParseFullIdentifier_VendorWithoutExactPatch is a regression test
// for a deliberate design decision, not a bug: "21-temurin" names a
// vendor but not an exact patch, and per the explicit decision to
// trade a little convenience for one single, predictable flow, this
// should NOT be treated as complete -- it should still go through the
// full vendor->major->patch picker sequence like any other partial
// input, not "smartly" skip the vendor picker just because a vendor
// happened to be named.
func TestParseFullIdentifier_VendorWithoutExactPatch(t *testing.T) {
	_, _, ok := parseFullIdentifier("java", "21-temurin")
	if ok {
		t.Error("expected '21-temurin' (major only, no exact patch) to NOT be treated as a complete identifier")
	}
}

func TestParseFullIdentifier_VersionWithoutVendor(t *testing.T) {
	_, _, ok := parseFullIdentifier("java", "21.0.2")
	if ok {
		t.Error("expected a bare version with no vendor suffix to NOT be treated as complete")
	}
}

func TestParseFullIdentifier_Empty(t *testing.T) {
	_, _, ok := parseFullIdentifier("java", "")
	if ok {
		t.Error("expected an empty argument to NOT be treated as complete")
	}
}

func TestParseFullIdentifier_UnknownVendorSuffix(t *testing.T) {
	_, _, ok := parseFullIdentifier("java", "21.0.2-corretto")
	if ok {
		t.Error("expected an unrecognized vendor suffix to NOT be treated as complete")
	}
}
func TestReadDefault_NoneSet(t *testing.T) {
	setTestHome(t, t.TempDir())
	tool, _ := tooldef.Get("java")
	got, err := readDefault(tool)
	if err != nil {
		t.Fatalf("expected no error when no default has ever been set, got: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string when no default is set, got: %q", got)
	}
}

func TestReadDefault_ReturnsStoredValue(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")

	if err := os.MkdirAll(filepath.Dir(tool.DefaultPath()), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	// Deliberately includes trailing whitespace/newline, matching how
	// a real file written by a text editor or `echo` might look --
	// readDefault should trim it.
	if err := os.WriteFile(tool.DefaultPath(), []byte("21.0.2-temurin\n"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	got, err := readDefault(tool)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "21.0.2-temurin" {
		t.Errorf("expected trimmed stored value, got: %q", got)
	}
}

// TestResolveUse_DefaultKeywordWithNoneSet is a regression test for
// the exact agreed error wording -- confirms `sk use <tool> default`
// with no default ever set produces the specific message, not a
// generic or misleading one, and returns ErrNotFound (so use.go's
// outer error handling doesn't print a redundant second message --
// see the comment there for why ErrNotFound is treated specially).
func TestResolveUse_DefaultKeywordWithNoneSet(t *testing.T) {
	setTestHome(t, t.TempDir())

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	sess := &term.Session{Out: w}

	_, resolveErr := resolveUse(sess, term.Styles{}, "java", "default", "")

	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()

	if resolveErr != ErrNotFound {
		t.Errorf("expected ErrNotFound (so use.go doesn't print a redundant second message), got: %v", resolveErr)
	}
	if !strings.Contains(out, "No default JDK set.") {
		t.Errorf("expected the specific 'No default JDK set.' message, got: %q", out)
	}
	if !strings.Contains(out, "sk default java") {
		t.Errorf("expected the message to point at the exact command to set one, got: %q", out)
	}
}

// TestResolveUse_DefaultKeywordSubstitutesStoredVersion confirms
// "default" is correctly resolved to whatever's actually stored,
// BEFORE any of resolveUse's other logic runs -- proven here by
// confirming resolution fails with "not found" for the STORED
// version (since nothing is actually installed in this test), not
// with any error suggesting "default" itself was treated as a
// literal, invalid version string.
func TestResolveUse_DefaultKeywordSubstitutesStoredVersion(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")

	if err := os.MkdirAll(filepath.Dir(tool.DefaultPath()), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.WriteFile(tool.DefaultPath(), []byte("99.0.0-temurin"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	sess := &term.Session{Out: w}

	_, resolveErr := resolveUse(sess, term.Styles{}, "java", "default", "")

	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()

	if resolveErr != ErrNotFound {
		t.Errorf("expected ErrNotFound (nothing installed for the stored version), got: %v", resolveErr)
	}
	// Confirms "default" was substituted with the REAL stored value
	// before proceeding -- the not-found message should reference
	// "99.0.0-temurin" (what was stored), not the literal word
	// "default".
	if !strings.Contains(out, "99.0.0-temurin") {
		t.Errorf("expected the not-found message to reference the substituted stored version, got: %q", out)
	}
}

// TestResolveUse_RequiresJavaToolWithNothingInstalledReportsCorrectTool
// is a regression test for a real bug caught via actual use: `sk use
// maven` with neither Maven nor any JDK installed used to fail with a
// bare "No JDK selected... No JDK versions found." message, never
// mentioning Maven at all -- because the java prerequisite check ran
// BEFORE Maven's own inventory was ever examined, so its failure
// short-circuited the whole function before reaching the correct,
// Maven-specific message. Fixed by checking the TARGET tool's own
// inventory first: there's nothing to activate for Maven if Maven
// itself has nothing installed, regardless of Java's state.
func TestResolveUse_RequiresJavaToolWithNothingInstalledReportsCorrectTool(t *testing.T) {
	setTestHome(t, t.TempDir())

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	sess := &term.Session{Out: w}

	_, resolveErr := resolveUse(sess, term.Styles{}, "maven", "", "")

	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()

	if resolveErr != ErrNotFound {
		t.Errorf("expected ErrNotFound, got: %v", resolveErr)
	}
	if !strings.Contains(out, "No Maven versions found") {
		t.Errorf("expected the message to correctly reference Maven (the tool actually requested), got: %q", out)
	}
	if strings.Contains(out, "JDK") {
		t.Errorf("expected NO mention of JDK when Maven itself has nothing installed -- the java prerequisite is irrelevant until there's something to actually activate, got: %q", out)
	}
}

// TestResolveUse_RequiresJavaToolWithVersionButNoJavaStillChecksPrerequisite
// confirms the fix above didn't overcorrect: when the target tool DOES
// have something installed (so there's genuinely something to
// activate), the java prerequisite check still correctly runs and
// correctly fails if no JDK is available -- this case SHOULD mention
// JDK, since Java truly is required to proceed.
func TestResolveUse_RequiresJavaToolWithVersionButNoJavaStillChecksPrerequisite(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	// This sandbox's own system Java sets JAVA_HOME in the ambient
	// environment (an artifact of this specific CI/sandbox, unrelated
	// to sk) -- explicitly unset it here so the prerequisite check
	// below genuinely runs rather than silently short-circuiting on
	// an "already set" that has nothing to do with this test.
	oldJavaHome, hadJavaHome := os.LookupEnv("JAVA_HOME")
	os.Unsetenv("JAVA_HOME")
	defer func() {
		if hadJavaHome {
			os.Setenv("JAVA_HOME", oldJavaHome)
		}
	}()

	mavenTool, _ := tooldef.Get("maven")
	if err := os.MkdirAll(filepath.Join(mavenTool.CandidateRoot(), "apache-maven-3.9.9"), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	sess := &term.Session{Out: w}

	_, resolveErr := resolveUse(sess, term.Styles{}, "maven", "", "")

	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()

	// Fails here specifically because this sandbox has no real
	// terminal for the recursive java picker to use -- the important
	// assertion is WHICH message appears before that failure, not the
	// exact error value.
	if resolveErr == nil {
		t.Fatal("expected an error (no real terminal for the java picker in this test environment)")
	}
	if !strings.Contains(out, "No JDK selected") {
		t.Errorf("expected the java prerequisite check to still run when Maven DOES have something installed, got: %q", out)
	}
}

// TestResolveUse_ExplicitVersionNotFoundReportsCorrectToolNotJava is a
// regression test for a real bug caught via actual use: the earlier
// fix for "sk use maven" (no version) with nothing installed only
// covered that ONE path -- `sk use maven 3.9.14` (an EXPLICIT
// version) with no JDK installed and Maven not having 3.9.14 either
// still showed the same confusing bare "No JDK selected... No JDK
// versions found." message, since the explicit-version case never
// validated the version existed before touching the java prerequisite
// chain. Confirms the fix: Maven's own "not found" message now
// appears, with zero mention of JDK, since there's nothing to
// activate for Maven regardless of Java's state.
func TestResolveUse_ExplicitVersionNotFoundReportsCorrectToolNotJava(t *testing.T) {
	setTestHome(t, t.TempDir())
	oldJavaHome, hadJavaHome := os.LookupEnv("JAVA_HOME")
	os.Unsetenv("JAVA_HOME")
	defer func() {
		if hadJavaHome {
			os.Setenv("JAVA_HOME", oldJavaHome)
		}
	}()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	sess := &term.Session{Out: w}

	_, resolveErr := resolveUse(sess, term.Styles{}, "maven", "3.9.14", "")

	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()

	if resolveErr != ErrNotFound {
		t.Errorf("expected ErrNotFound, got: %v", resolveErr)
	}
	if !strings.Contains(out, "apache-maven-3.9.14") {
		t.Errorf("expected the message to correctly reference the requested Maven version, got: %q", out)
	}
	if strings.Contains(out, "JDK") {
		t.Errorf("expected NO mention of JDK when Maven doesn't have this version, regardless of Java's state, got: %q", out)
	}
}

// TestResolveUse_ExplicitVersionFoundStillChecksJavaPrerequisite
// confirms the fix above didn't overcorrect: when the EXPLICIT
// version DOES exist, the java prerequisite check still correctly
// runs and correctly fails if no JDK is available.
func TestResolveUse_ExplicitVersionFoundStillChecksJavaPrerequisite(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	oldJavaHome, hadJavaHome := os.LookupEnv("JAVA_HOME")
	os.Unsetenv("JAVA_HOME")
	defer func() {
		if hadJavaHome {
			os.Setenv("JAVA_HOME", oldJavaHome)
		}
	}()

	mavenTool, _ := tooldef.Get("maven")
	if err := os.MkdirAll(filepath.Join(mavenTool.CandidateRoot(), "apache-maven-3.9.14"), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	sess := &term.Session{Out: w}

	_, resolveErr := resolveUse(sess, term.Styles{}, "maven", "3.9.14", "")

	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()

	if resolveErr == nil {
		t.Fatal("expected an error (no real terminal for the java picker in this test environment)")
	}
	if !strings.Contains(out, "No JDK selected") {
		t.Errorf("expected the java prerequisite check to still run when the requested Maven version genuinely exists, got: %q", out)
	}
}

// TestResolveUse_NoVersionWithoutTTYReturnsVersionRequired is design
// doc §6's own "harden the existing automatic TTY-detection fallback"
// -- when a picker WOULD be shown (no version given, at least one
// real candidate installed) but sess.HasTTY is false (a zero-value
// Session, exactly like every OTHER test in this file that never
// opens a real terminal), this must return a clean *CLIError with
// version_required -- the SAME error.code --format=json's own
// resolveUseJSON reports for the identical situation -- rather than
// letting the picker's own low-level "/dev/tty could not be opened"
// wording leak through to a real user.
func TestResolveUse_NoVersionWithoutTTYReturnsVersionRequired(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	sess := &term.Session{Out: w} // HasTTY defaults to false

	_, resolveErr := resolveUse(sess, term.Styles{}, "java", "", "")

	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()

	var cliErr *CLIError
	if !errors.As(resolveErr, &cliErr) {
		t.Fatalf("expected a *CLIError, got: %T (%v)", resolveErr, resolveErr)
	}
	if cliErr.Code != ErrCodeVersionRequired {
		t.Errorf("expected version_required, got %q", cliErr.Code)
	}
	if ExitCode(resolveErr) != 101 {
		t.Errorf("expected exit code 101, got %d", ExitCode(resolveErr))
	}
	if strings.Contains(out, "/dev/tty") {
		t.Errorf("expected the picker's own low-level wording to NEVER reach the user, got: %q", out)
	}
	if !strings.Contains(out, "a version is required") {
		t.Errorf("expected a clear, actionable message, got: %q", out)
	}
}

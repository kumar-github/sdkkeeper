package cli

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"sdkkeeper/internal/term"
)

// withSpinnerSession mirrors withSession exactly, except HasTTY is
// true -- runWithSpinner's real, animated path only runs when
// HasTTY is true, which normally requires a real terminal device.
// Since HasTTY is just a plain bool field on term.Session, setting it
// directly here exercises the real animated path without needing an
// actual pty in this sandbox.
func withSpinnerSession(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	oldSession, oldStyles := session, styles
	session = &term.Session{Out: w, HasTTY: true}
	styles = term.StylesForWriter(w)

	fn()

	w.Close()
	session, styles = oldSession, oldStyles

	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String()
}

// TestRunWithSpinner_FastWorkStillTakesAtLeastTheFloor is the
// regression test for the real, reported issue: a resolve call that
// finishes almost instantly used to draw and immediately erase the
// spinner, genuinely too fast to ever be seen -- confirmed directly
// (a real BellSoft resolve completing before the first render tick
// left nothing visible on screen at all). minSpinnerDuration exists
// specifically so that can't happen: total elapsed time must be at
// least the floor, even when the work itself was instant.
func TestRunWithSpinner_FastWorkStillTakesAtLeastTheFloor(t *testing.T) {
	start := time.Now()
	var err error
	withSpinnerSession(t, func() {
		err = runWithSpinner("resolving...", "resolved", func() error {
			return nil // instant -- the whole point of this test
		})
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if elapsed < minSpinnerDuration {
		t.Errorf("expected at least %v to elapse even for instant work, got %v", minSpinnerDuration, elapsed)
	}
}

// TestRunWithSpinner_SlowWorkNotDelayedFurther confirms the floor is
// a MINIMUM, not something added on top -- work that already takes
// longer than minSpinnerDuration must return promptly once it
// finishes, not wait for an additional floor period stacked on top.
func TestRunWithSpinner_SlowWorkNotDelayedFurther(t *testing.T) {
	workDuration := minSpinnerDuration + 200*time.Millisecond
	start := time.Now()
	withSpinnerSession(t, func() {
		_ = runWithSpinner("resolving...", "resolved", func() error {
			time.Sleep(workDuration)
			return nil
		})
	})
	elapsed := time.Since(start)
	// Generous upper bound (workDuration + 150ms slack for scheduling/
	// tick-interval overhead) -- not asserting a tight bound, just
	// that it isn't ALSO padded by another full floor's worth of
	// extra waiting on top of its own already-longer-than-the-floor
	// duration.
	if elapsed > workDuration+150*time.Millisecond {
		t.Errorf("expected roughly %v (work's own duration, not padded further), got %v", workDuration, elapsed)
	}
}

func TestRunWithSpinner_ReturnsWorkErrorAfterFloor(t *testing.T) {
	wantErr := errors.New("resolve failed")
	var gotErr error
	withSpinnerSession(t, func() {
		gotErr = runWithSpinner("resolving...", "resolved", func() error {
			return wantErr
		})
	})
	if gotErr != wantErr {
		t.Errorf("expected the exact error from work() to be returned, got: %v", gotErr)
	}
}

// TestRunWithSpinner_NonTTYSkipsFloorEntirely confirms the floor only
// applies to the real, animated path -- the non-interactive fallback
// (piped output, no real terminal) must return as fast as work()
// itself, with no artificial delay tax. Padding a piped/scripted
// invocation to "look nicer" would be a pure regression there; the
// floor exists to fix a HUMAN perception problem that doesn't apply
// when nothing is being animated in the first place.
func TestRunWithSpinner_NonTTYSkipsFloorEntirely(t *testing.T) {
	start := time.Now()
	withSession(t, func() { // HasTTY defaults to false here
		_ = runWithSpinner("resolving...", "resolved", func() error {
			return nil
		})
	})
	elapsed := time.Since(start)
	if elapsed >= minSpinnerDuration {
		t.Errorf("expected the non-TTY path to skip the floor entirely, took %v (floor is %v)", elapsed, minSpinnerDuration)
	}
}

// TestRunWithSpinner_SuccessPersistsDoneMessage confirms the
// regression fix: on success, the spinner's animated line is replaced
// by a PERSISTED completion line (doneMessage), matching how
// Downloading transitions to "Download complete ..." and Extracting
// to "Extraction complete (N files)" -- both stay on screen, not
// erased with nothing left behind the way the spinner used to behave.
func TestRunWithSpinner_SuccessPersistsDoneMessage(t *testing.T) {
	out := withSpinnerSession(t, func() {
		_ = runWithSpinner("Resolving JDK 27 (liberica)...", "Resolved JDK 27 (liberica)", func() error {
			return nil
		})
	})
	if !strings.Contains(out, "Resolved JDK 27 (liberica)") {
		t.Errorf("expected the persisted done message on success, got: %q", out)
	}
}

// TestRunWithSpinner_FailureClearsWithoutDoneMessage confirms the
// done message is NOT printed on failure -- only cleared, leaving a
// clean line for the caller's own error message, matching
// Downloading/Extracting's identical "clear the redrawn line before
// printing the error" behavior on their own failure paths.
func TestRunWithSpinner_FailureClearsWithoutDoneMessage(t *testing.T) {
	wantErr := errors.New("boom")
	var gotErr error
	out := withSpinnerSession(t, func() {
		gotErr = runWithSpinner("Resolving JDK 27 (liberica)...", "Resolved JDK 27 (liberica)", func() error {
			return wantErr
		})
	})
	if gotErr != wantErr {
		t.Errorf("expected the work error to be returned, got: %v", gotErr)
	}
	if strings.Contains(out, "Resolved JDK 27 (liberica)") {
		t.Errorf("expected NO done message on failure, got: %q", out)
	}
}

// TestRunWithSpinner_NonTTYAlsoPersistsDoneMessageOnSuccess confirms
// the non-interactive fallback matches too -- Downloading/Extracting
// both still print their own final "complete" line even without a
// real TTY to redraw on, so the spinner does the same for consistency.
func TestRunWithSpinner_NonTTYAlsoPersistsDoneMessageOnSuccess(t *testing.T) {
	out := withSession(t, func() { // HasTTY defaults to false here
		_ = runWithSpinner("Resolving...", "Resolved JDK 27 (liberica)", func() error {
			return nil
		})
	})
	if !strings.Contains(out, "Resolved JDK 27 (liberica)") {
		t.Errorf("expected the done message even without a real TTY, got: %q", out)
	}
}

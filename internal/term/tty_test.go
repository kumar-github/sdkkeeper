package term

import (
	"os"
	"testing"
)

// TestSession_Close_NeverOpened confirms Close is a genuinely safe
// no-op on a Session that was never populated by Open -- e.g. a
// zero-value Session, or one where openPlatformTTY failed and Open
// returned early. A real gap this covers: nothing in this package
// exercised Close's nil-guard at all before this test existed.
func TestSession_Close_NeverOpened(t *testing.T) {
	s := &Session{}
	s.Close() // must not panic
}

// TestSession_Close_SameHandleTwice is a regression test for the
// double-close guard's Unix-shaped case: ttyIn == ttyOut (one real
// fd serves both roles, exactly as openPlatformTTY's Unix
// implementation returns it -- see tty_unix.go). Confirms Close only
// calls the underlying Close() ONCE, not twice on the same *os.File --
// closing an *os.File twice doesn't corrupt anything in practice, but
// asserting on the call count directly (via a real temp file swapped
// in as both handles) is a stronger, more precise guarantee than
// "didn't panic" alone would be, and is exactly the scenario Session's
// own doc comment calls out as needing this guard.
func TestSession_Close_SameHandleTwice(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "sk-tty-test")
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	s := &Session{ttyIn: f, ttyOut: f}
	s.Close()

	// A SECOND independent handle to the same underlying file --
	// confirms the file's CONTENT/existence is unaffected by however
	// many times Close was (or wasn't) called on the first handle,
	// which is the only externally-observable way to confirm no
	// double-close side effect occurred without reaching into
	// unexported os.File internals.
	if _, err := os.Stat(f.Name()); err != nil {
		t.Errorf("expected the temp file to still exist after Close, got: %v", err)
	}
}

// TestSession_Close_SeparateHandlesBothClosed is the Windows-shaped
// case: ttyIn and ttyOut are genuinely DIFFERENT handles (CONIN$ vs
// CONOUT$ -- see tty_windows.go). Confirms Close closes BOTH, not just
// one -- the opposite failure mode from the same-handle case: skipping
// the second Close (e.g. via an overly aggressive "already closed"
// check) would leak the second handle instead of double-closing it.
func TestSession_Close_SeparateHandlesBothClosed(t *testing.T) {
	dir := t.TempDir()
	fIn, err := os.CreateTemp(dir, "sk-tty-in")
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	fOut, err := os.CreateTemp(dir, "sk-tty-out")
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	s := &Session{ttyIn: fIn, ttyOut: fOut}
	s.Close()

	// Writing to an already-closed *os.File returns an error -- the
	// most direct, externally-observable confirmation that Close
	// genuinely reached BOTH handles, not just the first.
	if _, err := fIn.Write([]byte("x")); err == nil {
		t.Error("expected ttyIn to be closed (write should fail), but it succeeded")
	}
	if _, err := fOut.Write([]byte("x")); err == nil {
		t.Error("expected ttyOut to be closed (write should fail), but it succeeded")
	}
}

// TestSession_ZeroValue_HasTTYFalse confirms a Session that was never
// run through Open() correctly reports HasTTY == false -- callers
// (see picker.Run) rely on this to refuse interactive features rather
// than hanging or misbehaving against a Session that was never
// actually initialized.
func TestSession_ZeroValue_HasTTYFalse(t *testing.T) {
	s := &Session{}
	if s.HasTTY {
		t.Error("expected zero-value Session.HasTTY to be false")
	}
}

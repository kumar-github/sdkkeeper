//go:build windows

package term

import (
	"os"
	"testing"
)

// TestOpenPlatformTTY_SignatureCompiles exists specifically so this
// sandbox -- which can cross-compile for Windows but cannot execute a
// Windows binary at all -- has a concrete, windows-tagged TEST file
// confirming `go vet`/`go test -c` (compile-only) succeeds under
// GOOS=windows, not just `go build`. This matters because a test-only
// mistake (e.g. a future windows-specific test helper referencing
// something Unix-only) would NOT be caught by `go build ./...` alone,
// since production and test compilation are separate steps.
//
// This does NOT confirm CONIN$/CONOUT$ actually behave as documented
// on a real Windows machine -- see openPlatformTTY's own doc comment
// in tty_windows.go for that still-open, first-priority verification.
func TestOpenPlatformTTY_SignatureCompiles(t *testing.T) {
	// Confirms openPlatformTTY's actual signature type-checks
	// correctly under this platform's build -- assigning it to an
	// explicitly-typed variable (rather than just referencing the
	// name) means a signature mismatch fails to COMPILE here, not
	// just fails a runtime assertion. Deliberately never CALLED --
	// invoking it would open a real console handle this sandbox
	// doesn't have, and isn't the point: the point is confirming the
	// declared type is well-formed.
	var fn func() (*os.File, *os.File, error) = openPlatformTTY
	_ = fn

	// Session's own fields (In/Out/HasTTY, all platform-neutral) are
	// exercised directly too, confirming tty.go's shared struct
	// compiles cleanly alongside the windows-specific half.
	s := &Session{}
	if s.HasTTY {
		t.Error("zero-value Session should report HasTTY == false")
	}
	s.Close() // must be a safe no-op on a never-Open'd Session
}

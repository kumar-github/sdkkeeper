//go:build windows

package term

import "os"

// openPlatformTTY opens Windows' two console special files -- CONIN$
// for keyboard input, CONOUT$ for screen output -- confirmed directly
// from Microsoft's own console API documentation: these are genuinely
// two distinct buffers, not one bidirectional stream the way a Unix
// tty device is, so (unlike the Unix side) this really does need two
// separate *os.File handles, not one reused for both roles.
//
// NOT verified on a real Windows machine yet -- this sandbox can only
// confirm this compiles under GOOS=windows cross-compilation (see
// tty_windows_test.go), not that CONIN$/CONOUT$ actually behave as
// documented in this specific Go runtime. Flagged as the first,
// highest-priority thing to confirm on a real Windows machine, the
// same way Temurin's and Liberica's live vendor APIs eventually were.
func openPlatformTTY() (in *os.File, out *os.File, err error) {
	in, err = os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	out, err = os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		in.Close()
		return nil, nil, err
	}
	return in, out, nil
}

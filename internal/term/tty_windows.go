//go:build windows

package term

import "os"

// openPlatformTTY opens Windows' two console special files -- CONIN$
// for keyboard input, CONOUT$ for screen output. Unlike Unix's single
// bidirectional tty device, these are two distinct buffers, so this
// needs two separate *os.File handles, not one reused for both roles.
//
// Not verified on a real Windows machine yet -- only confirmed to
// compile under GOOS=windows cross-compilation (see
// tty_windows_test.go).
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

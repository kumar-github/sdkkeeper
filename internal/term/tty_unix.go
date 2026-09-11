//go:build !windows

package term

import "os"

// openPlatformTTY opens /dev/tty, the Unix terminal device -- a single
// handle that serves as both the read (keyboard input) and write
// (screen output) side, confirmed directly from how every Unix tty
// device file has always worked: one file descriptor, opened O_RDWR,
// is both readable and writable. Returning the same *os.File for both
// in and out (rather than opening it twice) matches that reality
// exactly, and keeps Session.Close's double-close guard (ttyIn ==
// ttyOut) meaningful.
func openPlatformTTY() (in *os.File, out *os.File, err error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	return tty, tty, nil
}

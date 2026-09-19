//go:build !windows

package term

import "os"

// openPlatformTTY opens /dev/tty, the Unix terminal device -- one
// file descriptor, opened O_RDWR, serving both read and write, so the
// same *os.File is returned for both in and out (keeping Session.Close's
// double-close guard, ttyIn == ttyOut, meaningful).
func openPlatformTTY() (in *os.File, out *os.File, err error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	return tty, tty, nil
}

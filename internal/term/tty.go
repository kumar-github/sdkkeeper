// Package term isolates the terminal-access mechanics needed for both
// the interactive picker and status text to work correctly across
// shells, arrived at after three compounding bugs:
//
//  1. The picker was invisible from an interactive Nushell prompt
//     (reedline doesn't hand off terminal control to a child process
//     the way zsh does), even though the same code worked in
//     non-interactive script mode.
//  2. Fixed by writing directly to /dev/tty instead of inherited
//     stdin/stdout/stderr -- the same technique fzf and other robust
//     TUI tools use, to be immune to how a parent shell pipes or
//     captures stdio.
//  3. That fix broke colors: lipgloss's default renderer detects color
//     capability against os.Stdout, which often isn't a real terminal
//     once invoked through a shell wrapper's capture mechanism (eval
//     "$(...)" in zsh, `| complete` in Nushell). Fixed by pointing
//     lipgloss's renderer at the same /dev/tty handle.
//  4. That still didn't work: styles were package-level variables,
//     initialized before main() runs, before the renderer fix could
//     apply. Fixed by deferring style creation until after the
//     renderer is set.
//
// This package exists so the sequence is implemented in exactly one
// place. The platform-neutral Session type and Open/Close logic live
// here; the actual terminal-handle-acquisition mechanics live in
// tty_unix.go and tty_windows.go, behind the single openPlatformTTY
// function this file calls.
package term

import (
	"os"

	"github.com/charmbracelet/lipgloss"
)

// Session holds the terminal handles and status-output writer for one
// invocation of sk.
type Session struct {
	// Out is where all user-facing status/error text goes, and where
	// the picker renders. Points at the real terminal handle when
	// available; falls back to os.Stderr otherwise.
	Out *os.File

	// In is where the picker reads keyboard input from. On Unix this
	// is the same handle as Out (/dev/tty opened O_RDWR); on Windows
	// it's a genuinely separate handle (CONIN$ vs CONOUT$). Kept on
	// both platforms so callers can use it unconditionally.
	In *os.File

	// HasTTY is true if Out is a real, separately-opened terminal
	// handle, false if it fell back to plain os.Stderr/os.Stdin.
	HasTTY bool

	ttyIn  *os.File
	ttyOut *os.File
}

// Open acquires the terminal session for this process. Must be called
// once, early in main, before any styles are built or any picker is
// shown.
//
// SK_FORCE_NO_TTY, if set to any non-empty value, skips the
// openPlatformTTY attempt and returns the same plain os.Stderr/
// os.Stdin fallback used when no real terminal is available. This
// exists solely for scripts/sk-sequence-check.sh: /dev/tty is
// reachable from a subprocess whenever the calling session has a
// controlling terminal at all, regardless of that subprocess's own
// stdio redirection -- so no shell-level capture (command
// substitution, file redirection, piping) can intercept text written
// there. That means test assertions that capture sk's confirmation
// text only ever worked in non-interactive contexts (CI, sandboxed
// runs) with no controlling terminal at all; running the same script
// on a real terminal needs an explicit way to reach the same,
// already-correct fallback. setsid would normally do this, but isn't
// installed on macOS by default -- this env var avoids that
// dependency entirely.
func Open() *Session {
	s := &Session{Out: os.Stderr, In: os.Stdin}

	if os.Getenv("SK_FORCE_NO_TTY") != "" {
		return s
	}

	in, out, err := openPlatformTTY()
	if err != nil {
		// No real terminal available (CI, non-interactive invocation).
		return s
	}

	s.ttyIn, s.ttyOut = in, out
	s.In, s.Out = in, out
	s.HasTTY = true

	// Point lipgloss's renderer at the real terminal handle so its
	// color-capability detection matches the actual output target.
	lipgloss.SetDefaultRenderer(lipgloss.NewRenderer(out))

	return s
}

// Close releases the terminal handle(s), if any were opened. Safe to
// call when Open fell back to os.Stderr/os.Stdin.
//
// Guards against double-closing: on Unix, ttyIn == ttyOut (one fd
// serves both roles).
func (s *Session) Close() {
	if s.ttyIn == nil {
		return
	}
	s.ttyIn.Close()
	if s.ttyOut != s.ttyIn {
		s.ttyOut.Close()
	}
}

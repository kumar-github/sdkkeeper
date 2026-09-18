// Package term isolates the terminal-access mechanics proven necessary
// in design doc §6.4 -- the single hardest-won piece of the entire PoC
// phase, arrived at only after three compounding bugs:
//
//  1. The picker was invisible when invoked from an interactive Nushell
//     prompt (reedline does not reliably hand off terminal control to a
//     child process the way zsh does), even though the exact same code
//     worked correctly in non-interactive script mode.
//  2. Fixed by writing directly to /dev/tty instead of inherited
//     stdin/stdout/stderr -- the same technique fzf and most robust TUI
//     tools use, specifically to be immune to how a parent shell pipes
//     or captures stdio.
//  3. That fix broke colors: lipgloss's default renderer detects color
//     capability against os.Stdout, which is often not a real terminal
//     once invoked through a shell wrapper's capture mechanism (eval
//     "$(...)" in zsh, `| complete` in Nushell). Fixed by explicitly
//     pointing lipgloss's renderer at the same /dev/tty handle.
//  4. That fix initially didn't work either: styles were package-level
//     variables, initialized by Go before main() runs -- before the
//     renderer fix could possibly apply. Fixed by deferring style
//     creation until after the renderer is set.
//
// Every command that shows the interactive picker OR prints user-facing
// status text needs this exact sequence, in this exact order. This
// package exists so it is implemented, and can go wrong, in exactly one
// place -- not reproduced per-command the way the original PoC's single
// cmdUse() function did it inline.
//
// Platform split (added for Windows support): this file holds the
// platform-NEUTRAL Session type and Open/Close logic. The actual
// terminal-handle-acquisition mechanics live in tty_unix.go (build tag
// !windows) and tty_windows.go (build tag windows), behind the single
// openPlatformTTY function this file calls -- confirmed as the right
// seam directly from how bubbletea and mattn/go-tty (a real, widely
// used Go terminal library) both split their own equivalent code: one
// function per platform, same signature, selected entirely by the Go
// build system rather than a runtime OS check, so a mismatched build
// can never accidentally reach the wrong platform's syscalls at all.
package term

import (
	"os"

	"github.com/charmbracelet/lipgloss"
)

// Session holds the terminal handles and status-output writer for one
// invocation of sk. Callers get one Session (via Open) at the start of
// main, use it for everything user-facing, and Close it before exiting.
type Session struct {
	// Out is where ALL user-facing status/error text should go, AND
	// where the interactive picker renders -- not just status text.
	// Points at the real terminal's output handle when available;
	// falls back to os.Stderr otherwise (e.g. no real terminal -- CI,
	// non-interactive contexts).
	Out *os.File

	// In is where the interactive picker reads keyboard input from.
	// On Unix this is the SAME handle as Out (one fd serves both
	// roles there -- /dev/tty opened O_RDWR). On Windows it is
	// genuinely a SEPARATE handle (CONIN$ vs CONOUT$ -- Windows
	// console I/O is two distinct buffers, not one bidirectional
	// stream the way a Unix tty device is; confirmed directly from
	// Microsoft's own console API docs). Kept as its own field on
	// BOTH platforms, rather than only existing on Windows, so every
	// caller (see picker.Run) can use sess.In unconditionally without
	// its own platform check.
	In *os.File

	// HasTTY is true if Out is a real, separately-opened terminal
	// handle, false if it fell back to plain os.Stderr/os.Stdin.
	// Callers that need to know whether interactive features (the
	// picker) are even possible should check this rather than
	// assuming.
	HasTTY bool

	ttyIn  *os.File
	ttyOut *os.File
}

// Open acquires the terminal session for this process. Must be called
// once, early (as close to the start of main as possible), before any
// styles are built or any picker is shown -- see the package doc above
// for exactly why the ordering matters.
//
// SK_FORCE_NO_TTY, if set to any non-empty value, skips the
// openPlatformTTY attempt entirely and returns the same plain
// os.Stderr/os.Stdin fallback Open already uses when no real terminal
// is available. This exists SOLELY for scripts/sk-sequence-check.sh
// (and any similar automated harness), never meant to be set by a
// real user -- same "internal plumbing, not a real flag/setting"
// category as --shell-format (see root.go). The problem it solves is
// real and was found by actually running that script on a genuine
// terminal for the first time: openPlatformTTY opens /dev/tty
// directly (see tty_unix.go's own doc comment on why -- it's what
// makes the eval-based shell wrapper safe in the first place, by
// design), and /dev/tty is reachable whenever the CALLING PROCESS's
// session has a controlling terminal at all, completely independent
// of whatever that process's own stdin/stdout/stderr happen to be
// redirected to. That means NO shell-level capture technique --
// command substitution, file redirection, piping -- can ever
// intercept text written there, on any real, interactive terminal:
// every test-script assertion that captures sk's own confirmation
// text (`out=$(sk ...)`) and pattern-matches it had only ever been
// exercised in non-interactive contexts (this project's own CI,
// sandboxed test runs) where no controlling terminal exists at all,
// so the ALREADY-CORRECT, already-tested os.Stderr fallback path was
// the one being exercised the entire time -- silently, since nothing
// about that fallback's own behavior needed this env var to reach it
// in THOSE environments. The first real-terminal run surfaced this
// gap immediately. Rather than fight the OS to detach a subprocess's
// controlling terminal (fragile, and setsid -- the standard Unix tool
// for exactly that -- isn't even installed on macOS by default),
// this gives the test harness a direct, explicit way to reach the
// exact same, already-correct, already-passing fallback path
// deliberately, on any platform, with no external tool dependency at
// all.
func Open() *Session {
	s := &Session{Out: os.Stderr, In: os.Stdin}

	if os.Getenv("SK_FORCE_NO_TTY") != "" {
		return s
	}

	in, out, err := openPlatformTTY()
	if err != nil {
		// No real terminal available (CI, non-interactive invocation).
		// Fall back to plain os.Stderr/os.Stdin for status text; the
		// picker itself will separately need to handle HasTTY == false
		// by refusing to run interactively rather than hanging or
		// producing another silent-failure repeat of §6.4.
		return s
	}

	s.ttyIn, s.ttyOut = in, out
	s.In, s.Out = in, out
	s.HasTTY = true

	// lipgloss's default renderer detects color capability against
	// os.Stdout by default. Since real output now goes to the real
	// terminal handle, point the renderer at THAT handle so color
	// detection matches the actual output target -- see point 3 in
	// the package doc.
	lipgloss.SetDefaultRenderer(lipgloss.NewRenderer(out))

	return s
}

// Close releases the terminal handle(s), if any were opened. Safe to
// call even when Open fell back to os.Stderr/os.Stdin (HasTTY ==
// false) -- it's then simply a no-op.
//
// Guards against closing the same handle twice: on Unix, ttyIn ==
// ttyOut (one fd serves both roles), so closing both fields
// unconditionally would double-close a single, already-closed file
// descriptor -- harmless in practice (a second Close on an *os.File
// just returns an error, doesn't panic or corrupt anything), but
// checking directly is clearer than relying on that being true forever.
func (s *Session) Close() {
	if s.ttyIn == nil {
		return
	}
	s.ttyIn.Close()
	if s.ttyOut != s.ttyIn {
		s.ttyOut.Close()
	}
}

package cli

import "testing"

// TestParseShellFormat closes a real, pre-existing gap: parseShellFormat
// had no direct test at all before this -- only indirect coverage via
// whatever ShellFormat value happened to reach writeResult/
// writeDeactivate in other tests. Table-driven so every recognized
// value (including the "" zero-value case, and an unrecognized string)
// is verified explicitly, in one place.
func TestParseShellFormat(t *testing.T) {
	cases := map[string]ShellFormat{
		"json":       ShellJSON,
		"powershell": ShellPowerShell,
		// Deliberately fails OPEN to ShellZsh, not an error -- this
		// flag is internal plumbing a human never types directly
		// (see shellFormatFlag's own doc comment in root.go), so an
		// empty or unrecognized value must never hard-fail a real
		// invocation over something no user-facing documentation
		// ever described.
		"":        ShellZsh,
		"zsh":     ShellZsh,
		"bogus":   ShellZsh,
		"nushell": ShellZsh, // the real flag value is "json", not "nushell" -- confirms this isn't silently accepted too
	}
	for raw, want := range cases {
		if got := parseShellFormat(raw); got != want {
			t.Errorf("parseShellFormat(%q) = %v, want %v", raw, got, want)
		}
	}
}

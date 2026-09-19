package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ShellFormat identifies which shell's syntax writeResult/
// writeDeactivate should emit -- a small enum rather than a bool,
// since PowerShell support added a genuine third, structurally
// distinct output shape (neither zsh's eval-able commands nor
// Nushell's JSON).
type ShellFormat int

const (
	// ShellZsh is the default: plain, eval-able zsh/bash-style shell
	// commands, consumed directly by zsh's `eval "$(command sk "$@")"`.
	ShellZsh ShellFormat = iota

	// ShellJSON is Nushell's format -- it has no eval-equivalent, so
	// its wrapper parses a structured JSON payload instead.
	ShellJSON

	// ShellPowerShell is PowerShell's own syntax ($env:VAR = "value",
	// Remove-Item Env:VAR) -- see powershellFormatter for specifics.
	ShellPowerShell
)

// formatter builds the two shapes of machine-readable output this
// package emits, in one target shell's syntax. Each implementation
// (zshFormatter, jsonFormatter, powershellFormatter) is a pure,
// stateless function pair, independently unit-testable on its
// returned string.
type formatter interface {
	// formatResult builds the full activation script/payload for a
	// successful `use`: setting each env var in envVars, then
	// prepending newDirs to PATH (after first stripping any existing
	// entry starting with one of prefixes).
	formatResult(envVars map[string]string, newDirs, prefixes []string) string

	// formatDeactivate builds the script/payload that clears envVar
	// and removes binPath from PATH -- used by both `remove` (when
	// the removed version was active) and `use <tool> null`.
	formatDeactivate(envVar, binPath string) string
}

func formatterFor(format ShellFormat) formatter {
	switch format {
	case ShellJSON:
		return jsonFormatter{}
	case ShellPowerShell:
		return powershellFormatter{}
	default:
		return zshFormatter{}
	}
}

// reversedPaths returns dirs in reverse order. PathPrepends is built
// prerequisite-first (Java's bin, then Maven's), but the last one
// added is the more specific tool and should win PATH lookups, i.e.
// appear first in the final PATH.
func reversedPaths(prepends []PathPrepend) []string {
	out := make([]string, len(prepends))
	for i, p := range prepends {
		out[len(prepends)-1-i] = p.Dir
	}
	return out
}

// stripPrefixes collects the unique StripPrefix values across
// prepends, preserving first-seen order.
func stripPrefixes(prepends []PathPrepend) []string {
	seen := make(map[string]bool, len(prepends))
	var out []string
	for _, p := range prepends {
		if p.StripPrefix == "" || seen[p.StripPrefix] {
			continue
		}
		seen[p.StripPrefix] = true
		out = append(out, p.StripPrefix)
	}
	return out
}

// writeResult prints r to os.Stdout only, never sess.Out, in whichever
// format the calling shell wrapper expects -- the one place
// machine-readable output is emitted; every other message goes to
// sess.Out.
func writeResult(r *Result, format ShellFormat) {
	if r.isEmpty() {
		// Covers a nil or genuinely empty Result. A PARTIALLY
		// populated one (see resolveUse) is not empty and IS written,
		// even alongside an error -- a real prior success must not be
		// discarded by a later, unrelated cancellation.
		return
	}

	orderedPaths := reversedPaths(r.PathPrepends)
	prefixes := stripPrefixes(r.PathPrepends)
	fmt.Fprintln(os.Stdout, formatterFor(format).formatResult(r.EnvVars, orderedPaths, prefixes))
}

// writeDeactivate prints, to os.Stdout only, whichever shell commands
// clear envVar and strip binPath from PATH in the calling shell. Used
// by `remove` (when the deleted version was active) and `use <tool>
// null`. binPath must also be stripped, not just envVar unset --
// otherwise PATH could still resolve to an earlier, still-accumulated
// entry from a prior `sk use` call, even with the env var correctly
// cleared.
func writeDeactivate(envVar, binPath string, format ShellFormat) {
	if envVar == "" && binPath == "" {
		return
	}
	fmt.Fprintln(os.Stdout, formatterFor(format).formatDeactivate(envVar, binPath))
}

// shellQuote wraps s in single quotes for safe use as a literal POSIX
// shell argument, escaping an embedded quote via close-escape-reopen.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// powershellQuote is shellQuote's PowerShell counterpart -- also
// literal single quotes, but escapes an embedded quote by doubling it
// rather than POSIX's close-escape-reopen.
func powershellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// zshFormatter emits plain, eval-able zsh/bash-style shell commands.
type zshFormatter struct{}

func (zshFormatter) formatResult(envVars map[string]string, newDirs, prefixes []string) string {
	var b strings.Builder
	for k, v := range envVars {
		fmt.Fprintf(&b, "export %s=%q\n", k, v)
	}
	b.WriteString(zshPathAssignmentScript(newDirs, prefixes))
	return b.String()
}

func (zshFormatter) formatDeactivate(envVar, binPath string) string {
	var b strings.Builder
	if envVar != "" {
		fmt.Fprintf(&b, "unset %s\n", envVar)
	}
	if binPath != "" {
		// Uses only zsh built-ins (no external tr/grep/sed process --
		// those can fail to even be found if PATH at this moment is
		// entirely sk-managed bin directories) and zsh's explicit
		// ${(s.:.)PATH} split syntax, since zsh doesn't word-split
		// unquoted expansions on IFS the way bash does.
		fmt.Fprintf(&b, "_sk_new_path=\"\"\nfor _sk_dir in ${(s.:.)PATH}; do\n  if [ \"$_sk_dir\" != %s ]; then\n    if [ -z \"$_sk_new_path\" ]; then _sk_new_path=\"$_sk_dir\"; else _sk_new_path=\"$_sk_new_path:$_sk_dir\"; fi\n  fi\ndone\nexport PATH=\"$_sk_new_path\"\nunset _sk_new_path _sk_dir\n", shellQuote(binPath))
	}
	return strings.TrimRight(b.String(), "\n")
}

// zshPathAssignmentScript builds the zsh commands that strip any
// existing PATH entry starting with one of prefixes, then prepend
// newDirs -- ensuring at most one sk-managed entry per tool ever
// persists on PATH, however many times a version is switched.
func zshPathAssignmentScript(newDirs, prefixes []string) string {
	if len(prefixes) == 0 {
		// Nothing on PATH to strip yet -- plain prepend is simpler.
		return fmt.Sprintf("export PATH=%q", strings.Join(newDirs, ":")+":$PATH")
	}

	quotedPrefixes := make([]string, len(prefixes))
	for i, p := range prefixes {
		quotedPrefixes[i] = shellQuote(p) + "/*"
	}
	pattern := strings.Join(quotedPrefixes, "|")

	return fmt.Sprintf(
		"_sk_new_path=\"\"\nfor _sk_dir in ${(s.:.)PATH}; do\n  case \"$_sk_dir\" in\n    %s) ;;\n    *)\n      if [ -z \"$_sk_new_path\" ]; then _sk_new_path=\"$_sk_dir\"; else _sk_new_path=\"$_sk_new_path:$_sk_dir\"; fi\n      ;;\n  esac\ndone\nexport PATH=%q\nunset _sk_new_path _sk_dir",
		pattern, strings.Join(newDirs, ":")+":$_sk_new_path",
	)
}

// jsonFormatter emits Nushell's structured payload, since it has no
// eval-equivalent to consume raw shell syntax.
type jsonFormatter struct{}

func (jsonFormatter) formatResult(envVars map[string]string, newDirs, prefixes []string) string {
	payload := map[string]interface{}{}
	for k, v := range envVars {
		payload[k] = v
	}
	payload["PATH_PREPEND"] = strings.Join(newDirs, ":")
	if len(prefixes) > 0 {
		payload["PATH_STRIP_PREFIXES"] = prefixes
	}
	data, _ := json.Marshal(payload)
	return string(data)
}

func (jsonFormatter) formatDeactivate(envVar, binPath string) string {
	payload := map[string][]string{}
	if envVar != "" {
		payload["unset"] = []string{envVar}
	}
	if binPath != "" {
		payload["path_remove"] = []string{binPath}
	}
	data, _ := json.Marshal(payload)
	return string(data)
}

// powershellFormatter emits PowerShell's own syntax. Like zsh, it
// produces literal, executable commands (via Invoke-Expression), but
// genuinely PowerShell's own: $env:VARNAME = "value" sets a variable;
// there's no built-in "unset", so Remove-Item on Env:VARNAME clears
// one; $env:Path is semicolon-separated, not POSIX's colon; and
// -split/-join/Where-Object replaces zsh's array-splitting loop.
type powershellFormatter struct{}

func (powershellFormatter) formatResult(envVars map[string]string, newDirs, prefixes []string) string {
	var b strings.Builder
	for k, v := range envVars {
		fmt.Fprintf(&b, "$env:%s = %s\n", k, powershellQuote(v))
	}
	b.WriteString(powershellPathAssignmentScript(newDirs, prefixes))
	return b.String()
}

func (powershellFormatter) formatDeactivate(envVar, binPath string) string {
	var b strings.Builder
	if envVar != "" {
		fmt.Fprintf(&b, "Remove-Item Env:%s -ErrorAction SilentlyContinue\n", envVar)
	}
	if binPath != "" {
		fmt.Fprintf(&b, "$env:Path = (($env:Path -split ';') | Where-Object { $_ -ne %s }) -join ';'\n", powershellQuote(binPath))
	}
	return strings.TrimRight(b.String(), "\n")
}

// powershellPathAssignmentScript mirrors zshPathAssignmentScript's
// purpose (strip existing PATH entries matching a prefix, then
// prepend newDirs), using PowerShell's own -like wildcard matching
// inside a Where-Object filter.
func powershellPathAssignmentScript(newDirs, prefixes []string) string {
	newPath := powershellQuote(strings.Join(newDirs, ";"))
	if len(prefixes) == 0 {
		return fmt.Sprintf("$env:Path = %s + ';' + $env:Path", newPath)
	}

	var conditions strings.Builder
	for i, p := range prefixes {
		if i > 0 {
			conditions.WriteString(" -or ")
		}
		fmt.Fprintf(&conditions, "$_ -like (%s + '\\*')", powershellQuote(p))
	}

	return fmt.Sprintf(
		"$_sk_new_path = (($env:Path -split ';') | Where-Object { -not (%s) }) -join ';'\n$env:Path = %s + ';' + $_sk_new_path\nRemove-Variable _sk_new_path",
		conditions.String(), newPath,
	)
}

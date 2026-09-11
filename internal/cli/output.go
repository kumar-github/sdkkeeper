package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ShellFormat identifies which shell's syntax writeResult/
// writeDeactivate should emit. Replaces an earlier boolean --json
// flag that could only ever represent two states -- adding Windows/
// PowerShell support meant a genuine THIRD, structurally distinct
// output shape was needed (neither zsh's plain eval-able commands nor
// Nushell's JSON), and a bool has no room for a third value without
// either overloading its meaning or bolting on a second flag next to
// it. A small enum, threaded through from a single string flag (see
// root.go), scales to a future fourth shell as one new case, not a
// second flag.
type ShellFormat int

const (
	// ShellZsh is the default: plain, eval-able zsh/bash-style shell
	// commands (export VAR=value, etc.) -- zsh's own `eval
	// "$(command sk "$@")"` wrapper consumes this directly.
	ShellZsh ShellFormat = iota

	// ShellJSON is Nushell's format -- Nushell has no eval-equivalent
	// at all, so its wrapper parses a structured JSON payload instead
	// (see design doc §6.3).
	ShellJSON

	// ShellPowerShell is PowerShell's own syntax ($env:VAR = "value",
	// Remove-Item Env:VAR) -- PowerShell DOES have a real eval
	// equivalent (Invoke-Expression), but its syntax is genuinely its
	// own, not zsh's -- see powershellFormatter's own comment for the
	// concrete differences (semicolon PATH separator, no built-in
	// "unset", etc.).
	ShellPowerShell
)

// formatter builds the two shapes of machine-readable output this
// package ever emits, in one target shell's own syntax. Each concrete
// implementation (zshFormatter, jsonFormatter, powershellFormatter) is
// a pure, stateless function pair -- no I/O, trivially unit-testable
// on the returned STRING directly, unlike the earlier design where
// format-specific logic was interleaved with the actual os.Stdout
// printing inside writeResult/writeDeactivate themselves.
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

// reversedPaths returns dirs in reverse order. PathPrepends is built up
// in "prerequisite first, dependent tool last" order (e.g. Java's bin
// added first via the RequiresJava merge, Maven's own bin appended
// after) -- but the LAST one added is the more specific tool and should
// win PATH lookups, i.e. appear FIRST in the final PATH. This mirrors
// the original shell scripts' behavior: each sequential `export
// PATH=...` further down the chain naturally took priority over an
// earlier one, since each one prepended onto whatever PATH already was.
func reversedPaths(prepends []PathPrepend) []string {
	out := make([]string, len(prepends))
	for i, p := range prepends {
		out[len(prepends)-1-i] = p.Dir
	}
	return out
}

// stripPrefixes collects the unique StripPrefix values across prepends,
// preserving first-seen order -- the set of "any earlier sk-managed
// entry starting with one of these should be removed before the new
// dirs are prepended" prefixes for this Result.
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

// writeResult prints r to os.Stdout -- and ONLY os.Stdout, never
// sess.Out -- in whichever format the calling shell wrapper expects.
// This is the one place that machine-readable output is emitted; every
// other message in this package goes to sess.Out (see term.Session).
func writeResult(r *Result, format ShellFormat) {
	if r.isEmpty() {
		// Covers both a literal nil and a genuinely empty Result (e.g.
		// an unknown tool name, or a cancellation before anything was
		// ever resolved) -- nothing worth applying, so nothing is
		// written. A PARTIALLY populated Result (see resolveUse) is
		// NOT considered empty and IS written, even when accompanied
		// by an error -- that's the actual fix for the bug where a
		// successful JDK selection was being discarded by a later,
		// unrelated cancellation.
		return
	}

	orderedPaths := reversedPaths(r.PathPrepends)
	prefixes := stripPrefixes(r.PathPrepends)
	fmt.Fprintln(os.Stdout, formatterFor(format).formatResult(r.EnvVars, orderedPaths, prefixes))
}

// writeDeactivate prints, to os.Stdout ONLY -- never sess.Out, same
// separation writeResult's own doc comment describes -- whichever
// shell commands (a) clear envVar and (b) strip binPath out of PATH,
// in the CALLING shell. Used both by `remove` (when the version it
// just deleted was active in the same shell) and by `use <tool> null`.
//
// binPath must ALSO be stripped, not just envVar unset -- a real gap
// found via actual use: `sk use` prepends a version's bin directory
// to PATH every time it's called, but never removes an EARLIER
// prepend when a DIFFERENT version is later activated in the same
// shell session. Once the removed version's PATH entry is (correctly)
// skipped as nonexistent, the shell simply continues down PATH and
// resolves e.g. `java` to whatever's next -- which could be an
// earlier, still-valid sk-managed entry from a PRIOR `sk use` call in
// this same session, not "nothing". Clearing the env var alone was
// not enough to fix the actual symptom: `java -version` (PATH-
// resolved, not env-var-resolved) kept reporting a DIFFERENT,
// unexpected JDK, even though `sk current java` correctly reported
// nothing active.
func writeDeactivate(envVar, binPath string, format ShellFormat) {
	if envVar == "" && binPath == "" {
		return
	}
	fmt.Fprintln(os.Stdout, formatterFor(format).formatDeactivate(envVar, binPath))
}

// shellQuote wraps s in single quotes for safe use as a literal
// argument in POSIX shell (zsh/bash) output -- single quotes are the
// standard POSIX-safe way to pass an arbitrary string through
// untouched, with any literal single quote inside it escaped by
// closing the quote, inserting an escaped one, then reopening. JDK
// install paths essentially never contain spaces or quotes in
// practice, but this makes that an assumption the code doesn't
// silently rely on.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// powershellQuote is shellQuote's PowerShell counterpart -- PowerShell
// single-quoted strings are ALSO literal (no variable interpolation,
// matching POSIX single quotes' own purpose here), but escape an
// embedded single quote by DOUBLING it, not POSIX's close-escape-
// reopen trick -- confirmed directly from PowerShell's own quoting
// rules documentation. Using single, not double, quotes specifically
// avoids PowerShell interpreting a literal "$" in a path as the start
// of variable interpolation.
func powershellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// zshFormatter emits plain, eval-able zsh/bash-style shell commands --
// the format this package has always produced, now extracted into its
// own pure function pair rather than interleaved with os.Stdout
// printing.
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
		// NOT external tools (tr/grep/sed), and NOT shell parameter
		// expansion (${PATH//search/replace}) -- two real, severe
		// bugs caught in sequence via actual end-to-end testing in a
		// real zsh process:
		//
		//  1. ${PATH//search/replace} uses "/" as ITS OWN delimiter
		//     between search and replace, fundamentally incompatible
		//     with a file path AS the search text, since a path
		//     inherently contains slashes too -- confirmed to
		//     silently produce catastrophically garbled PATH output,
		//     not even an error.
		//  2. The tr/grep -Fx/sed pipeline that replaced it depends
		//     on those external tools being FINDABLE ON THE CURRENT
		//     PATH at the moment the pipeline runs -- but modifying
		//     PATH is the whole point. If PATH at that moment doesn't
		//     happen to include the directory those tools live in
		//     (confirmed directly: happens whenever PATH consists
		//     only of sk-managed bin directories, not an exotic edge
		//     case), the pipeline fails to even find tr/grep/sed,
		//     produces empty output, and PATH gets wiped out
		//     ENTIRELY -- catastrophically worse than the original
		//     bug this whole mechanism exists to fix.
		//
		// This zsh-native array-splitting loop uses ONLY shell
		// built-ins (no external process spawned at all), so it
		// cannot fail this way regardless of what PATH happens to
		// contain when it runs. ${(s.:.)PATH} is zsh's own explicit
		// "split on this character" syntax -- deliberately NOT a
		// generic `for x in $PATH` loop, since zsh (unlike bash) does
		// NOT word-split unquoted expansions on IFS by default, so
		// that would silently iterate the whole PATH as one item
		// (confirmed directly, a real, separate zsh-specific gotcha
		// found while fixing this).
		fmt.Fprintf(&b, "_sk_new_path=\"\"\nfor _sk_dir in ${(s.:.)PATH}; do\n  if [ \"$_sk_dir\" != %s ]; then\n    if [ -z \"$_sk_new_path\" ]; then _sk_new_path=\"$_sk_dir\"; else _sk_new_path=\"$_sk_new_path:$_sk_dir\"; fi\n  fi\ndone\nexport PATH=\"$_sk_new_path\"\nunset _sk_new_path _sk_dir\n", shellQuote(binPath))
	}
	return strings.TrimRight(b.String(), "\n")
}

// zshPathAssignmentScript builds the zsh commands that (1) strip any
// EXISTING PATH entry starting with one of prefixes, then (2) prepend
// newDirs -- ensuring at most one sk-managed entry per tool ever
// persists on PATH, regardless of how many times a tool's version is
// switched or re-activated within one session.
//
// A real, confirmed bug this exists to fix: without this, `sk use`
// only ever PREPENDED, never cleaning up an earlier prepend for the
// same tool -- switching JDKs (or the same activation firing more than
// once, e.g. the documented "OPTIONAL auto-activation" shell snippet
// running on each new shell) silently accumulated MULTIPLE entries
// for the same tool on PATH. The direct, observed consequence:
// removing the currently-active JDK correctly cleared JAVA_HOME and
// stripped that ONE specific entry (see writeDeactivate), but `java`
// still resolved to whichever EARLIER, still-accumulated entry came
// next on PATH -- looking exactly like "removing the active JDK
// doesn't work", even though the removed entry itself genuinely was
// gone.
func zshPathAssignmentScript(newDirs, prefixes []string) string {
	if len(prefixes) == 0 {
		// No tool being activated has anything currently on PATH to
		// strip (e.g. the very first `sk use` in a fresh shell) --
		// the plain, unconditional prepend is correct and simpler.
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

// jsonFormatter emits Nushell's structured payload -- Nushell has no
// eval-equivalent, so its wrapper parses this directly (see design
// doc §6.3) instead of consuming raw shell syntax.
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

// powershellFormatter emits PowerShell's own syntax. PowerShell DOES
// have a real eval equivalent (Invoke-Expression -- see
// init.ps1.tmpl), so, like zsh, this produces literal, directly
// executable commands rather than a structured payload for the
// wrapper to parse -- but the syntax itself is genuinely PowerShell's
// own, confirmed directly from Microsoft's own documentation, not a
// re-skinned copy of the zsh formatter:
//
//   - $env:VARNAME = "value" sets an environment variable (the direct
//     structural counterpart to zsh's `export VAR=value`).
//   - PowerShell has NO built-in "unset" -- an env var is removed via
//     Remove-Item on the Env: PSDrive (Env:VARNAME), a genuinely
//     different mechanism, not just different syntax for the same
//     operation.
//   - $env:Path is semicolon-separated on Windows (";" -- confirmed
//     directly from how Windows' own PATH environment variable has
//     always been delimited, unlike POSIX's ":"), so every join/split
//     operation below uses ";", not zsh's ":".
//   - PowerShell's own -split/-join/Where-Object pipeline replaces
//     zsh's ${(s.:.)PATH} array-splitting loop -- PowerShell arrays
//     and pipelines are native, idiomatic PowerShell, the direct
//     counterpart to reaching for zsh's own built-in array syntax
//     rather than external tools there.
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
// exact PURPOSE (strip any existing PATH entry starting with one of
// prefixes, then prepend newDirs) -- see that function's own comment
// for the full, real bug this dedup behavior fixes, which applies
// identically on Windows. The MECHANISM is genuinely PowerShell's own:
// -like's "*" wildcard (PowerShell's own glob syntax, the direct
// counterpart to zsh's case-pattern "/*" suffix matching) against each
// prefix, inside a Where-Object filter -- not a transliteration of the
// zsh script's literal syntax, which wouldn't parse as PowerShell at
// all.
func powershellPathAssignmentScript(newDirs, prefixes []string) string {
	newPath := powershellQuote(strings.Join(newDirs, ";"))
	if len(prefixes) == 0 {
		// Same reasoning as zshPathAssignmentScript's own early
		// return: nothing to strip yet, so the plain, unconditional
		// prepend is correct and simpler.
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

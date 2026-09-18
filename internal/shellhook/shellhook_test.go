package shellhook

import (
	"strings"
	"testing"
)

// TestGet_KnownShells closes a real, pre-existing gap: this package
// had NO tests at all before this. Table-driven so "powershell" and
// its "pwsh" alias (added for Windows support) sit alongside the
// previously-untested "zsh"/"nu" cases, all verified the same way --
// confirms each resolves to non-empty content without erroring, and
// that "pwsh" and "powershell" return the IDENTICAL script (true
// aliases, not two different templates that happen to both work).
func TestGet_KnownShells(t *testing.T) {
	for _, shell := range []string{"zsh", "nu", "powershell", "pwsh"} {
		script, err := Get(shell)
		if err != nil {
			t.Errorf("Get(%q) returned error: %v", shell, err)
		}
		if script == "" {
			t.Errorf("Get(%q) returned empty script", shell)
		}
	}
}

func TestGet_PwshIsAnAliasForPowershell(t *testing.T) {
	pwsh, err := Get("pwsh")
	if err != nil {
		t.Fatalf("Get(\"pwsh\") failed: %v", err)
	}
	powershell, err := Get("powershell")
	if err != nil {
		t.Fatalf("Get(\"powershell\") failed: %v", err)
	}
	if pwsh != powershell {
		t.Error("expected \"pwsh\" and \"powershell\" to return the identical script")
	}
}

func TestGet_UnsupportedShellReturnsError(t *testing.T) {
	if _, err := Get("fish"); err == nil {
		t.Error("expected an error for an unsupported shell")
	}
}

func TestGet_EmptyShellReturnsError(t *testing.T) {
	if _, err := Get(""); err == nil {
		t.Error("expected an error for an empty shell name")
	}
}

// TestGet_PowershellScriptUsesPowerShellSyntax confirms the returned
// script is genuinely PowerShell -- not, say, an accidentally
// misrouted copy of one of the other two templates. Checking for a
// PowerShell-specific token (Invoke-Expression has no equivalent
// spelling in zsh or Nushell) is a real, meaningful assertion, not
// just "non-empty".
func TestGet_PowershellScriptUsesPowerShellSyntax(t *testing.T) {
	script, err := Get("powershell")
	if err != nil {
		t.Fatalf("Get(\"powershell\") failed: %v", err)
	}
	if !strings.Contains(script, "Invoke-Expression") {
		t.Errorf("expected the PowerShell script to contain \"Invoke-Expression\", got: %s", script)
	}
	if !strings.Contains(script, "sk.exe") {
		t.Errorf("expected the PowerShell script to bypass itself via \"sk.exe\", got: %s", script)
	}
}

// TestGet_NuScriptClearsEnvViaNullNotHideEnv is a regression test for
// a real, live-reported bug found only by actually running the
// generated wrapper against a real nu process: Nushell's own hide-env
// silently fails to persist back to the caller whenever any OTHER
// $env.X = ... mutation also happens in the same --env function call
// -- confirmed directly, and reproduced even with hide-env called
// strictly last, so not an ordering issue. This wrapper mutates
// $env.PATH in the very same call whenever a version is deactivated,
// so hide-env silently did nothing in exactly the real scenario this
// whole branch exists for -- `sk use <tool> null` appeared to work
// (no crash, no error) but JAVA_HOME stayed genuinely visible to a
// real external process the entire time. Fixed by using a
// null-valued load-env instead, confirmed directly (against a real
// external process, not just Nushell's own $env record) to be
// correctly treated as absent.
func TestGet_NuScriptClearsEnvViaNullNotHideEnv(t *testing.T) {
	script, err := Get("nu")
	if err != nil {
		t.Fatalf("Get(\"nu\") failed: %v", err)
	}
	if strings.Contains(script, "hide-env $name") {
		t.Error("expected the nu script to NOT call hide-env $name -- confirmed directly against a real nu process to silently fail to persist alongside any other $env mutation in the same call")
	}
	if !strings.Contains(script, "load-env {($name): null}") {
		t.Errorf("expected the nu script to clear env vars via a null-valued load-env instead, got: %s", script)
	}
}

// TestGet_NuScriptNormalizesBareNullArgument is a regression test for
// a real, live-reported bug found only by actually running the
// generated wrapper against a real nu process: Nushell's own parser
// converts a bare, unquoted `null` word into its OWN `nothing` type
// at PARSE TIME, not the literal string "null" this wrapper's
// [...args] rest capture expects -- `sk use java null` (this
// project's own, deliberately designed "clear active state" syntax)
// crashed outright through this wrapper alone with "can't convert
// nothing to string", confirmed directly against a real nu process
// (zsh and PowerShell were never affected, since neither has this
// parsing behavior).
func TestGet_NuScriptNormalizesBareNullArgument(t *testing.T) {
	script, err := Get("nu")
	if err != nil {
		t.Fatalf("Get(\"nu\") failed: %v", err)
	}
	if !strings.Contains(script, `if ($a == null) { "null" } else { $a }`) {
		t.Errorf("expected the nu script to normalize a nothing-typed rest arg back to the literal string \"null\", got: %s", script)
	}
}

// TestGet_NuScriptSurfacesStderr is a regression test for a real,
// live-reported bug found only by actually running the real wrapper:
// `sk use maven` (nothing installed) and `sk use java` (a picker
// failure) both showed NOTHING at all through this wrapper -- no
// error, no message, completely silent -- even though the identical
// binary, run directly rather than through this wrapper, correctly
// printed both. Root cause: `| complete` captures BOTH stdout and
// stderr into the result record, and nothing ever printed
// $result.stderr -- so every human-readable status/error message sk
// writes (which falls back to plain stderr whenever a real
// controlling terminal isn't available to it, exactly the case for
// sk spawned this way) was silently discarded, regardless of whether
// stdout also happened to parse as JSON.
func TestGet_NuScriptSurfacesStderr(t *testing.T) {
	script, err := Get("nu")
	if err != nil {
		t.Fatalf("Get(\"nu\") failed: %v", err)
	}
	if !strings.Contains(script, "$result.stderr") {
		t.Error("expected the nu script to reference $result.stderr somewhere, so error/status messages are never silently discarded")
	}
	if !strings.Contains(script, `print -e ($result.stderr | str trim)`) {
		t.Errorf("expected the nu script to print $result.stderr unconditionally, got: %s", script)
	}
}

// TestGet_ZshScriptGuardsFormatFlagBeforeEval is a regression test for
// a real, found gap, confirmed directly against a real zsh process:
// the eval dispatch used to key off $1 alone, so `sk use --format=json
// java 21...` (flag AFTER the subcommand) reached
// `eval "$(command sk "$@")"` with sk's own raw JSON object as its
// input -- `sk --format=json use ...` (flag BEFORE) was accidentally
// safe only because $1 wasn't literally "use" in that case.
// --format=json must never reach eval regardless of where it appears.
func TestGet_ZshScriptGuardsFormatFlagBeforeEval(t *testing.T) {
	script, err := Get("zsh")
	if err != nil {
		t.Fatalf("Get(\"zsh\") failed: %v", err)
	}
	if !strings.Contains(script, `"$arg" == "--format" || "$arg" == --format=*`) {
		t.Errorf("expected the zsh script to scan for --format anywhere in \"$@\", got: %s", script)
	}
	if !strings.Contains(script, `if [[ "$has_format" -eq 0 && ( "$1" == "use" || "$1" == "remove" ) ]]`) {
		t.Errorf("expected the eval branch to be gated on has_format being 0, got: %s", script)
	}
}

// TestGet_NuScriptDeclaresFormatFlagExplicitly is a regression test
// for a real, PRE-EXISTING gap, confirmed directly against a real nu
// 0.101 process, unrelated to and predating --format's own
// introduction: Nushell's parser rejects ANY --flag/--flag=value
// token passed to a custom command that doesn't explicitly declare
// it, with a parse-time "nu::parser::unknown_flag" error, before this
// script's own function body ever runs at all -- confirmed against
// the ORIGINAL, unmodified script too, so `sk --format=json use ...`
// through this wrapper was ALREADY completely broken before --format
// was ever added to this project, regardless of position. Declaring
// --format explicitly in the signature fixes this outright, and
// Nushell's own named-flag parsing (confirmed directly) then extracts
// it correctly no matter where it appears among the other arguments.
func TestGet_NuScriptDeclaresFormatFlagExplicitly(t *testing.T) {
	script, err := Get("nu")
	if err != nil {
		t.Fatalf("Get(\"nu\") failed: %v", err)
	}
	if !strings.Contains(script, "--format: string") {
		t.Errorf("expected the nu script to declare --format explicitly in its signature, got: %s", script)
	}
	if !strings.Contains(script, `if ($format != null) {`) {
		t.Errorf("expected the nu script to branch on $format being set, got: %s", script)
	}
	if !strings.Contains(script, "^sk --format $format ...$args") {
		t.Errorf("expected the nu script to pass --format straight through, never via eval/--shell-format=json, got: %s", script)
	}
}

// TestGet_PowershellScriptGuardsFormatFlagBeforeInvokeExpression is a
// regression test for a real, found gap, confirmed directly against a
// real pwsh 7.4 process: the Invoke-Expression dispatch used to key
// off $args[0] alone, so `sk use --format=json java 21...` (flag
// AFTER the subcommand) reached Invoke-Expression with sk's own raw
// JSON object as its input -- confirmed to fail outright with a real
// PowerShell parse error ("Unexpected token ... in expression or
// statement").
func TestGet_PowershellScriptGuardsFormatFlagBeforeInvokeExpression(t *testing.T) {
	script, err := Get("powershell")
	if err != nil {
		t.Fatalf("Get(\"powershell\") failed: %v", err)
	}
	if !strings.Contains(script, `$a -eq "--format" -or $a -like "--format=*"`) {
		t.Errorf("expected the PowerShell script to scan $args for --format, got: %s", script)
	}
	if !strings.Contains(script, "if (-not $hasFormat -and $args.Count -ge 1") {
		t.Errorf("expected the Invoke-Expression branch to be gated on -not $hasFormat, got: %s", script)
	}
}

package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"sdkkeeper/internal/shellhook"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init <shell|skrc>",
		Short: "Print shell integration for zsh/nu/powershell, or create .skrc in this directory (sk init skrc)",
		Long: `Print shell integration for zsh, nu, or powershell (pwsh is accepted as an alias for powershell) --
or, with 'sk init skrc', create .skrc in the CURRENT directory from whatever
tools are currently active in this shell (same convention as 'git init'/'npm
init' -- run it while standing in $HOME for a personal, global fallback; run
it in a project directory to pin that project). See 'sk remove skrc' to
delete the nearest one, and 'sk use' with no arguments to apply it. The rest
of this text covers only the shell-integration form.

  zsh:        eval "$(sk init zsh)"          # in .zshrc
  nu:         sk init nu | save -f ($nu.data-dir | path join "vendor/autoload/sk.nu")
  powershell: sk init powershell | Out-File -Append $PROFILE

PREREQUISITE, for all three shells: the real sk binary (sk.exe on
Windows) must ALREADY be somewhere on your PATH before any of this
works. Every wrapper function above calls the real binary by NAME
only (never a hardcoded path), specifically so moving/upgrading the
binary later never requires touching your shell config again -- but
that means, without it on PATH, you'll see a plain "command not
found" (zsh/nu) or "is not recognized" (PowerShell) with no other
clue what's wrong. Confirm with 'which sk' (zsh), 'which sk' (nu), or
'Get-Command sk.exe' (PowerShell) before assuming anything else is
broken.

This defines the 'sk' wrapper function itself -- required for 'sk use'
and 'sk remove' to actually activate/clear anything in your shell (the
real binary only ever PRINTS the commands; the wrapper is what evals
them). Always required, regardless of anything below.

---

WINDOWS/POWERSHELL: a real, Windows-specific gotcha to check first

PowerShell's execution policy can silently block this ENTIRE
mechanism -- confirmed directly from Microsoft's own documentation:
running a script (including $PROFILE itself) needs at least
RemoteSigned, and this restriction does not apply to macOS or Linux at
all (a Windows-only concern). Check your current policy with:

    Get-ExecutionPolicy

If it reports "Restricted", set it (as an administrator, once) with:

    Set-ExecutionPolicy RemoteSigned -Scope CurrentUser

Skipping this check would mean $PROFILE silently never runs at all --
no error, no 'sk' wrapper function, 'sk use'/'sk remove' visibly doing
nothing. Confirm the wrapper actually loaded with a fresh PowerShell
window and 'Get-Command sk' before assuming anything else is wrong.

---

OPTIONAL: automatic activation of a stored default on every new shell

By design, sk does NOT do this automatically -- 'sk default <tool>
<version>' only stores a preference; applying it always requires an
explicit 'sk use <tool> default', every time, in every shell. If you
specifically want SDKMAN-style automatic activation instead, add this
yourself -- one line per tool you want auto-activated (there's no
single command that activates every tool's default at once, since the
right order between unrelated tools isn't something sk can guess on
your behalf).

  zsh (in .zshrc, AFTER the 'sk init zsh' line above):
    eval "$(sk use java default 2>/dev/null)"
    eval "$(sk use maven default 2>/dev/null)"

  nu: create a SEPARATE file (never edit sk.nu itself -- re-running
  the 'save -f' command above would silently wipe out anything you'd
  added to it). Vendor-autoload files load in alphabetical order, so
  this needs a name that sorts AFTER "sk.nu" (confirmed directly:
  "sk_defaults.nu" does, "sk-defaults.nu" does not -- the underscore
  matters):

    "sk use java default\n" | save -f ($nu.data-dir | path join "vendor/autoload/sk_defaults.nu")

  powershell (in $PROFILE, AFTER the 'sk init powershell' line above):
    sk use java default 2>$null | Out-Null

Note: since 'sk use'/'sk default's own status text writes directly to
the terminal device (not regular stdout), redirecting stderr away
(2>/dev/null, 2>$null) does NOT suppress it -- you will see either a
real confirmation or a "no default set" message on every new shell,
for every tool you add this for. That's a known, accepted trade-off,
not a bug.`,
		Args: requireArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] == "skrc" {
				if outputFormat == FormatJSON {
					data, jerr := buildInitSkrcJSON()
					return emitJSON(data, jerr)
				}
				return runInitSkrc()
			}

			script, err := shellhook.Get(args[0])
			if err != nil {
				return err
			}
			fmt.Print(script)
			return nil
		},
	}
}

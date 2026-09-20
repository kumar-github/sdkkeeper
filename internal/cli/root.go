// Package cli wires the tooldef/inventory/term/picker packages together
// into actual cobra commands. This package intentionally contains the
// only code in the project that knows about cobra, flags, and
// command-line argument parsing -- everything it calls into is plain,
// reusable Go with no CLI-framework dependency of its own.
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"sdkkeeper/internal/term"
)

// shellFormatFlag backs --shell-format, replacing an earlier boolean
// --json flag. Deliberately NOT given a Usage string that would show
// it in --help -- see design doc: this flag is internal plumbing
// between a shell wrapper (Nushell, PowerShell) and this binary,
// never meant to be typed by a user directly (a real bug was hit and
// fixed when the original PoC's usage text advertised --json as if it
// were a normal user-facing option).
//
// A bool could only ever represent two states -- adding PowerShell
// support meant a genuine THIRD, structurally distinct output shape
// was needed (see ShellFormat's own doc comment in output.go), and a
// second boolean flag next to the first would have meant two flags
// answering what is really ONE question ("which shell is calling
// me?"), the same "more than one way to do the same thing" this
// project's own stated design principle rejects for user-facing
// flags -- kept true here too, even though this specific flag is
// hidden, rather than treating hidden-ness as a reason to be less
// careful about it.
var shellFormatFlag string

// parseShellFormat turns the raw --shell-format string into a
// ShellFormat, defaulting to ShellZsh (zsh's plain eval-able commands)
// for both the empty string (the flag's own zero value, i.e. no
// wrapper set it at all -- true for direct, non-wrapped invocations)
// and any value it doesn't recognize, rather than erroring -- this
// flag is internal plumbing a human never types directly, so failing
// unrecognized input open to the original, long-standing zsh format
// is safer than a confusing hard error over something no user-facing
// documentation ever described.
func parseShellFormat(raw string) ShellFormat {
	switch raw {
	case "json":
		return ShellJSON
	case "powershell":
		return ShellPowerShell
	default:
		return ShellZsh
	}
}

// session is opened once per invocation, in the root command's
// PersistentPreRunE -- not per-command -- since §6.4 established that
// /dev/tty handling needs to happen in exactly one consistent place.
// Every command's RunE reads this rather than opening its own.
var session *term.Session
var styles term.Styles

// Execute builds and runs the root command, and returns whatever error
// the executed subcommand produced (nil on success). Deliberately does
// NOT call os.Exit itself -- that decision belongs to main(), which can
// then let this function's own return (and any defers in main) unwind
// normally before exiting, rather than an os.Exit deep in a command's
// RunE skipping cleanup further up the stack.
//
// version and commit are passed through from main.go rather than read
// directly by this package, so cli stays the only package that knows
// about cobra while main.go stays the only package that knows how
// those strings themselves get set (build-time ldflags injection). No
// longer wired to cobra's own Version field -- see newVersionCmd for
// why.
func Execute(version, commit string) error {
	root := &cobra.Command{
		Use:           "sk",
		Short:         "SDK Keeper — explicit, no-default SDK version switching",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			session = term.Open()
			styles = session.Styles()
			return nil
		},
		PersistentPostRun: func(cmd *cobra.Command, args []string) {
			session.Close()
		},
	}

	root.PersistentFlags().StringVar(&shellFormatFlag, "shell-format", "", "")
	root.PersistentFlags().MarkHidden("shell-format")

	// --format is the stable, documented, external-facing counterpart
	// to --shell-format -- not hidden, since it's meant to be
	// discovered via `sk --help`. A custom pflag.Value rather than a
	// plain StringVar so an unrecognized value is a hard parse-time
	// error instead of being silently accepted.
	root.PersistentFlags().Var(formatFlagValue{}, "format", `output format for machine consumption ("json")`)

	root.AddCommand(newUseCmd())
	root.AddCommand(newListCmd())
	root.AddCommand(newInitCmd())
	root.AddCommand(newAddCmd())
	root.AddCommand(newInstallCmd())
	root.AddCommand(newDefaultCmd())
	root.AddCommand(newRemoveCmd())
	root.AddCommand(newSearchCmd())
	root.AddCommand(newCurrentCmd())
	root.AddCommand(newVendorsCmd())
	root.AddCommand(newToolsCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newVersionCmd(version, commit))

	err := root.Execute()
	printUnreportedError(err)
	return err
}

// printUnreportedError catches the one class of error that NEVER gets
// a chance to print itself: cobra's own "unknown command"/"unknown
// flag" errors, generated while cobra is still figuring out WHICH
// command to run at all, before any of this project's own RunE code
// -- the only code that ever prints anything for an error -- gets a
// chance to run. A real, confirmed bug: with SilenceErrors true and
// main.go itself never printing anything either (by design -- see its
// own doc comment), `sk something` or `sk --bogus-flag` failed
// completely silently, exit code 1, zero output.
//
// Cobra doesn't expose a typed/sentinel error for this specific case
// (both are plain fmt.Errorf from cobra's own source), so this
// necessarily checks the message text itself -- narrow, deliberately
// only matching cobra's own/pflag's own known prefixes for this exact
// category, so it can never accidentally re-print an error a command's
// own RunE already handled and printed itself.
//
// "invalid argument " is pflag's own prefix (flag.go's
// `invalid argument %q for %q flag: %v`) for a registered flag's Set
// method rejecting its value -- added specifically for --format's own
// closed-enum validation (see formatFlagValue.Set): that failure, like
// an unknown command/flag, happens during cobra's flag-parsing pass,
// strictly BEFORE PersistentPreRunE ever runs, so it would otherwise
// be silently swallowed by SilenceErrors exactly like the other two
// cases this function already existed to catch.
//
// Same term.StylesForWriter(os.Stderr) technique as requireArgs, for
// the identical reason: this runs after root.Execute() returns, but
// an unknown-command/unknown-flag/invalid-flag-value error is detected
// by cobra BEFORE PersistentPreRunE ever runs (same as Args
// validation), so session/styles are genuinely still nil here too.
func printUnreportedError(err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "unknown command") && !strings.HasPrefix(msg, "unknown flag:") && !strings.HasPrefix(msg, "unknown shorthand flag:") && !strings.HasPrefix(msg, "invalid argument ") {
		return
	}
	s := term.StylesForWriter(os.Stderr)
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, s.Error.Render(fmt.Sprintf("\u2717 %s", msg)))
}

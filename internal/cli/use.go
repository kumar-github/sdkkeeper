package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

func newUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <tool> [version|null]",
		Short: "Activate a version of a tool for this shell session",
		Args:  requireArgs(cobra.RangeArgs(1, 2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			toolName := args[0]
			versionArg := ""
			if len(args) == 2 {
				versionArg = args[1]
			}

			// "null" is a distinct shape from a real version or
			// "default" -- see clearActive's own doc comment --
			// short-circuits BEFORE resolveUse entirely, rather than
			// being handled inside it the way "default" is.
			if versionArg == "null" {
				return clearActive(session, styles, toolName, parseShellFormat(shellFormatFlag))
			}

			result, err := resolveUse(session, styles, toolName, versionArg, "")

			// Always write whatever WAS resolved, even on failure --
			// this preserves a real prior success (e.g. a JDK
			// genuinely selected) even if a LATER, separate step (e.g.
			// Maven's own picker) was cancelled or failed.
			writeResult(result, parseShellFormat(shellFormatFlag))

			// Any confirmation message that was never folded into a
			// later picker's banner (because nothing further ran after
			// it), OR that WAS shown as a banner but is being
			// re-affirmed here so it reaches the user's permanent
			// scrollback, gets printed now, cleanly, once all
			// interactive steps are fully done. A blank line separates
			// each one from the PREVIOUS one (e.g. JDK + Maven) so
			// they don't run together -- but NOT before the first
			// message, so a single confirmation sits immediately after
			// the command, matching `add`/`list`'s tighter style
			// (confirmed inconsistent otherwise via a real screenshot:
			// this used to print a blank line unconditionally, even
			// for the single-message case, which the original
			// reasoning -- separating this from the eval'd `export
			// ...` lines -- didn't actually justify, since those lines
			// are captured entirely by the shell's $(...) substitution
			// and never appear on screen at all).
			// Exactly one blank line separates the command from its
			// output, consistently across every command in this tool
			// (use, list, add, install) -- including here before each
			// message, so a multi-step chain (e.g. JDK + Maven) keeps
			// the same one-line rhythm between each confirmation too.
			if result != nil && len(result.Messages) > 0 {
				for _, m := range result.Messages {
					fmt.Fprintln(session.Out)
					fmt.Fprintln(session.Out, m)
				}
			}

			if err != nil {
				// ErrCancelled/ErrNotFound already printed their own
				// user-facing message at the point of failure, inside
				// resolveUse -- only genuinely unexpected errors
				// (unknown tool, filesystem errors, etc.) need a
				// separate message printed here.
				if !errors.Is(err, ErrCancelled) && !errors.Is(err, ErrNotFound) {
					fmt.Fprintln(session.Out, styles.Error.Render(err.Error()))
				}
				return err
			}

			return nil
		},
	}
}

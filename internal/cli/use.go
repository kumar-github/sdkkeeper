package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"sdkkeeper/internal/inventory"
	"sdkkeeper/internal/tooldef"
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

			// --format=json is a completely separate, non-interactive
			// path (design doc §2/§6) -- it never touches the picker,
			// the RequiresJava banner-chaining machinery, or
			// session.Out at all, so it's branched off here, before
			// any of that runs, rather than threaded through
			// resolveUse itself. `use <tool> null` (deactivation) is
			// deliberately NOT handled here -- design doc §2's action
			// enum has no "deactivated" value, and clearing an env var
			// for the CURRENT shell isn't something a JSON blob printed
			// to stdout can express or accomplish either way; it falls
			// through to the existing clearActive path unchanged.
			if outputFormat == FormatJSON && versionArg != "null" {
				data, jerr := resolveUseJSON(toolName, versionArg)
				return emitJSON(data, jerr)
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

// resolveUseJSON is `use`'s --format=json resolution -- pure,
// side-effect-free (beyond the one os.LookupEnv read below), no
// session.Out, no picker. Deliberately independent of resolveUse
// rather than threading a JSON branch through it: resolveUse's
// banner/Messages/RequiresJava-chaining machinery exists entirely to
// solve an INTERACTIVE rendering problem (see resolveUse's own DESIGN
// NOTE) that simply doesn't exist here, and reusing it as-is would
// mean either dragging session.Out calls into the JSON path or
// littering resolveUse itself with format checks -- both worse than
// one small, independently-testable function that mirrors its logic
// exactly where it actually needs to.
func resolveUseJSON(toolName, versionArg string) (*activationPayload, *jsonError) {
	tool, ok := tooldef.Get(toolName)
	if !ok {
		return nil, &jsonError{Code: ErrCodeAmbiguousTool, Message: fmt.Sprintf("unknown tool: %s", toolName)}
	}

	// "default" occupies the same argument slot a real version would
	// (see resolveUse's own comment on this convention) -- resolved
	// here, once, exactly like the interactive path does.
	if versionArg == "default" {
		stored, err := readDefault(tool)
		if err != nil {
			return nil, &jsonError{Code: ErrCodeInternalError, Message: err.Error()}
		}
		if stored == "" {
			return nil, &jsonError{Code: ErrCodeNotFound, Message: fmt.Sprintf("no default %s set", tool.DisplayName)}
		}
		versionArg = stored
	}

	// No version given at all -- the interactive path would launch a
	// picker here (or, with zero candidates installed, report nothing
	// to select); --format=json can do neither (design doc §6), so
	// this is unconditionally version_required.
	if versionArg == "" {
		return nil, &jsonError{Code: ErrCodeVersionRequired, Message: fmt.Sprintf("a version is required for %s in --format=json (the interactive picker cannot be shown)", tool.DisplayName)}
	}

	// The interactive path's RequiresJava handling resolves a missing
	// prerequisite by recursively launching ITS OWN picker -- not
	// available here either. Rather than guess which JDK an agent
	// would want, this requires JAVA_HOME to already be set (i.e. a
	// prior `sk use java <version>` already ran), matching the exact
	// same "JAVA_HOME already set" fast-path resolveUse itself checks
	// first, before ever considering its own picker.
	if tool.RequiresJava {
		if _, alreadySet := os.LookupEnv("JAVA_HOME"); !alreadySet {
			return nil, &jsonError{Code: ErrCodeVersionRequired, Message: "no JDK selected (JAVA_HOME not set) -- the interactive picker cannot be shown in --format=json; run `sk use java <version>` first"}
		}
	}

	v, ok := inventory.Find(tool, versionArg)
	if !ok {
		return nil, &jsonError{Code: ErrCodeNotFound, Message: fmt.Sprintf("%s%s not found", tool.FolderPrefix, versionArg)}
	}

	return &activationPayload{
		Tool:    tool.Name,
		Version: v.Number,
		Vendor:  vendorOf(tool.Name, v.Number),
		Action:  string(actionActivated),
	}, nil
}

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

			// --format=json is a separate, non-interactive path: it
			// never touches the picker, banner-chaining, or
			// session.Out. `use <tool> null` (deactivation) falls
			// through unchanged -- there's no JSON shape for it, and
			// a printed JSON blob can't clear a shell var either way.
			if outputFormat == FormatJSON && versionArg != "null" {
				data, jerr := resolveUseJSON(toolName, versionArg)
				return emitJSON(data, jerr)
			}

			// "null" short-circuits before resolveUse entirely --
			// see clearActive's own doc comment.
			if versionArg == "null" {
				return clearActive(session, styles, toolName, parseShellFormat(shellFormatFlag))
			}

			result, err := resolveUse(session, styles, toolName, versionArg, "")

			// Always write whatever WAS resolved, even on failure --
			// preserves a real prior success (e.g. a JDK genuinely
			// selected) if a later step (e.g. Maven's own picker)
			// was cancelled or failed.
			writeResult(result, parseShellFormat(shellFormatFlag))

			// Print any confirmation not yet flushed to the user's
			// permanent scrollback. A blank line separates each from
			// the previous one, but not before the first, so a
			// single confirmation sits right after the command.
			if result != nil && len(result.Messages) > 0 {
				for _, m := range result.Messages {
					fmt.Fprintln(session.Out)
					fmt.Fprintln(session.Out, m)
				}
			}

			if err != nil {
				// ErrCancelled/ErrNotFound and a *CLIError from
				// versionRequiredNoTTY already printed their own
				// message where they occurred; only genuinely
				// unexpected errors need one here.
				var cliErr *CLIError
				if !errors.Is(err, ErrCancelled) && !errors.Is(err, ErrNotFound) && !errors.As(err, &cliErr) {
					fmt.Fprintln(session.Out, styles.Error.Render(err.Error()))
				}
				return err
			}

			return nil
		},
	}
}

// usePayload is `use`'s --format=json success shape: the shared
// {tool, version, vendor, action} fields plus EnvVar, EnvValue, and
// BinPath. --format=json can never mutate the calling shell's
// environment -- these three fields are what let a caller (an agent,
// or a wrapper script) reproduce the effect itself:
//
//	export $envVar=$envValue
//	export PATH="$binPath:$PATH"
//
// without needing any private knowledge of sk's own directory layout.
type usePayload struct {
	Tool     string  `json:"tool"`
	Version  string  `json:"version"`
	Vendor   *string `json:"vendor"`
	Action   string  `json:"action"`
	EnvVar   string  `json:"envVar"`
	EnvValue string  `json:"envValue"`
	BinPath  string  `json:"binPath"`
}

// resolveUseJSON is use's --format=json resolution: pure, no
// session.Out, no picker. Kept independent of resolveUse rather than
// threading a JSON branch through it, since resolveUse's banner/
// RequiresJava-chaining logic solves a purely interactive rendering
// problem this path doesn't have.
func resolveUseJSON(toolName, versionArg string) (*usePayload, *jsonError) {
	tool, ok := tooldef.Get(toolName)
	if !ok {
		return nil, &jsonError{Code: ErrCodeAmbiguousTool, Message: fmt.Sprintf("unknown tool: %s", toolName)}
	}

	// "default" occupies the same argument slot a real version
	// would, exactly like the interactive path.
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

	// No version given -- the interactive path would show a picker;
	// this can't, so it's unconditionally version_required.
	if versionArg == "" {
		return nil, &jsonError{Code: ErrCodeVersionRequired, Message: fmt.Sprintf("a version is required for %s in --format=json (the interactive picker cannot be shown)", tool.DisplayName)}
	}

	// A missing RequiresJava prerequisite would normally resolve via
	// its own picker; here it requires JAVA_HOME to already be set.
	if tool.RequiresJava {
		if _, alreadySet := os.LookupEnv("JAVA_HOME"); !alreadySet {
			return nil, &jsonError{Code: ErrCodeVersionRequired, Message: "no JDK selected (JAVA_HOME not set) -- the interactive picker cannot be shown in --format=json; run `sk use java <version>` first"}
		}
	}

	v, ok := inventory.Find(tool, versionArg)
	if !ok {
		return nil, &jsonError{Code: ErrCodeNotFound, Message: fmt.Sprintf("%s%s not found", tool.FolderPrefix, versionArg)}
	}

	return &usePayload{
		Tool:     tool.Name,
		Version:  v.Number,
		Vendor:   vendorOf(tool.Name, v.Number),
		Action:   string(actionActivated),
		EnvVar:   tool.EnvVar,
		EnvValue: tool.HomePath(v.Path),
		BinPath:  tool.BinPath(v.Path),
	}, nil
}

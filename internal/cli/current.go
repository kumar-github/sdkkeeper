package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"sdkkeeper/internal/inventory"
	"sdkkeeper/internal/tooldef"
)

// findActiveVersion returns the Version whose HomePath matches
// currentEnvValue, if any. Forward-transforms each known version the
// same way `use` did when it set the variable, rather than reverse-
// parsing the value (fragile -- e.g. java's darwin Contents/Home
// suffix).
func findActiveVersion(tool tooldef.Tool, versions []inventory.Version, currentEnvValue string) (inventory.Version, bool) {
	for _, v := range versions {
		if tool.HomePath(v.Path) == currentEnvValue {
			return v, true
		}
	}
	return inventory.Version{}, false
}

func newCurrentCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "current <tool>",
		Short:   "Show what's currently active for a tool in this shell session",
		Example: `  sk current java`,
		Args:    requireArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			toolName := args[0]

			if outputFormat == FormatJSON {
				data, jerr := buildCurrentJSON(toolName)
				return emitJSON(data, jerr)
			}

			tool, err := requireTool(toolName)
			if err != nil {
				return err
			}

			current, isSet := os.LookupEnv(tool.EnvVar)
			fmt.Fprintln(session.Out)
			if !isSet || current == "" {
				fmt.Fprintln(session.Out, styles.Neutral.Render(fmt.Sprintf("No %s currently active in this shell.", tool.DisplayName)))
				return nil
			}

			versions, err := inventory.Scan(tool)
			if err != nil {
				fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf("\u2717 Could not check installed versions: %s", err)))
				return err
			}
			if v, ok := findActiveVersion(tool, versions, current); ok {
				fmt.Fprintln(session.Out, styles.Neutral.Render(
					fmt.Sprintf("%s %s is currently active (%s=%s)", tool.DisplayName, v.Number, tool.EnvVar, current),
				))
				return nil
			}

			// Set, but doesn't match any known version -- e.g. set
			// manually, or removed after activation. Still useful to
			// show the raw value rather than claim nothing is active.
			fmt.Fprintln(session.Out, styles.Neutral.Render(
				fmt.Sprintf("%s=%s (does not match any version SDK Keeper currently knows about)", tool.EnvVar, current),
			))
			return nil
		},
	}
}

// activeVersionPayload/currentData are current's --format=json
// success shape. Active is a pointer so "nothing active" serializes
// as JSON null, a valid non-error state.
type activeVersionPayload struct {
	Version string  `json:"version"`
	Vendor  *string `json:"vendor"`
}

type currentData struct {
	Tool   string                `json:"tool"`
	Active *activeVersionPayload `json:"active"`
}

// buildCurrentJSON reports active: null both when nothing is set and
// when the env var doesn't match any known version -- the schema has
// no third shape for an unmatched raw value.
func buildCurrentJSON(toolName string) (*currentData, *jsonError) {
	tool, ok := tooldef.Get(toolName)
	if !ok {
		return nil, &jsonError{Code: ErrCodeAmbiguousTool, Message: fmt.Sprintf("unknown tool: %s", toolName)}
	}

	current, isSet := os.LookupEnv(tool.EnvVar)
	if !isSet || current == "" {
		return &currentData{Tool: tool.Name, Active: nil}, nil
	}

	versions, err := inventory.Scan(tool)
	if err != nil {
		return nil, &jsonError{Code: ErrCodeInternalError, Message: err.Error()}
	}
	v, ok := findActiveVersion(tool, versions, current)
	if !ok {
		return &currentData{Tool: tool.Name, Active: nil}, nil
	}

	return &currentData{
		Tool: tool.Name,
		Active: &activeVersionPayload{
			Version: v.Number,
			Vendor:  vendorOf(tool.Name, v.Number),
		},
	}, nil
}

package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"sdkkeeper/internal/inventory"
	"sdkkeeper/internal/tooldef"
)

// findActiveVersion returns the Version whose HomePath matches
// currentEnvValue, if any -- extracted as its own function specifically
// so this matching logic can be tested directly, without needing a
// real terminal session. Forward-transforms each known version the
// exact same way `use` did when it originally set this variable,
// rather than trying to reverse-parse the env var's value back into a
// version number (fragile -- would need to know exactly how to strip
// vendor/OS-specific suffixes, like the java-on-darwin Contents/Home
// path).
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

			// The env var IS set, but doesn't match any version sk
			// currently knows about -- e.g. set manually outside sk,
			// or the matching version was `sk remove`d after being
			// activated earlier in this same shell session. Still
			// genuinely useful to show the raw value, rather than
			// claim nothing is active when something clearly is.
			fmt.Fprintln(session.Out, styles.Neutral.Render(
				fmt.Sprintf("%s=%s (does not match any version SDK Keeper currently knows about)", tool.EnvVar, current),
			))
			return nil
		},
	}
}

// activeVersionPayload and currentData are `current`'s --format=json
// success `data` shape (design doc §3). Active is a pointer so
// "nothing active" serializes as a literal JSON null, exactly
// matching the doc's own second sample -- explicitly called out there
// as "a valid, non-error state", so buildCurrentJSON below never
// returns a jsonError for this case.
type activeVersionPayload struct {
	Version string  `json:"version"`
	Vendor  *string `json:"vendor"`
}

type currentData struct {
	Tool   string                `json:"tool"`
	Active *activeVersionPayload `json:"active"`
}

// buildCurrentJSON is `current`'s --format=json counterpart. When the
// tool's env var is set but doesn't match any version sk currently
// recognizes (e.g. set manually outside sk, or removed after being
// activated earlier in this shell), this reports active: null rather
// than the raw, unrecognized value -- design doc §3's schema only
// defines {version, vendor} or null for `active`, with no third shape
// for an unmatched raw value, so this stays strictly within what's
// actually documented rather than inventing an undocumented field.
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

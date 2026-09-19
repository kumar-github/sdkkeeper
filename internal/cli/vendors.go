package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"sdkkeeper/internal/tooldef"
)

func newVendorsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "vendors <tool>",
		Short:   "Show which vendors a tool can be installed from",
		Example: `  sk vendors java`,
		Args:    requireArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			toolName := args[0]

			if outputFormat == FormatJSON {
				data, jerr := buildVendorsJSON(toolName)
				return emitJSON(data, jerr)
			}

			tool, err := requireTool(toolName)
			if err != nil {
				return err
			}

			// Tied to whether this tool has any registered install
			// provider, matching install.go's own restriction rather
			// than a hardcoded per-tool check.
			if len(vendorNamesFor(tool.Name)) == 0 {
				fmt.Fprintln(session.Out)
				fmt.Fprintln(session.Out, styles.Neutral.Render(
					fmt.Sprintf("No vendors available for %s yet — install is not yet supported for this tool.", tool.DisplayName),
				))
				return nil
			}

			fmt.Fprintln(session.Out)
			fmt.Fprintln(session.Out, styles.Header.Render(fmt.Sprintf("Vendors for %s:", tool.DisplayName)))
			for _, name := range vendorNamesFor(tool.Name) {
				fmt.Fprintf(session.Out, "  %s\n", capitalize(name))
			}
			return nil
		},
	}
}

// vendorsData is vendors' --format=json success shape. A tool with
// zero registered providers reports an empty array, status ok -- that
// is a plain fact, not a failure.
type vendorsData struct {
	Tool    string   `json:"tool"`
	Vendors []string `json:"vendors"`
}

// buildVendorsJSON always succeeds once the tool name is recognized.
func buildVendorsJSON(toolName string) (*vendorsData, *jsonError) {
	tool, ok := tooldef.Get(toolName)
	if !ok {
		return nil, &jsonError{Code: ErrCodeAmbiguousTool, Message: fmt.Sprintf("unknown tool: %s", toolName)}
	}
	names := vendorNamesFor(tool.Name)
	if names == nil {
		names = []string{}
	}
	return &vendorsData{Tool: tool.Name, Vendors: names}, nil
}

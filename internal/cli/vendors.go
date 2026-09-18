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

			// Tied directly to what `install` can actually do for this
			// tool, checked generically (does this tool have ANY
			// registered provider?) rather than a hardcoded "is this
			// java?" check -- matches install.go's own exact
			// restriction and stays correct automatically as more
			// tools gain install support.
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

// vendorsData is `vendors`'s --format=json success `data` shape
// (design doc §3). A tool with zero registered providers reports an
// empty "vendors" array, status "ok" -- "no vendors available yet"
// is a plain fact about that tool, not a failure (same "valid,
// non-error state" reasoning design doc §3 gives current's `active:
// null`), so this deliberately does NOT return a jsonError for that
// case, matching buildVendorsJSON's own doc comment below.
type vendorsData struct {
	Tool    string   `json:"tool"`
	Vendors []string `json:"vendors"`
}

// buildVendorsJSON is `vendors`'s --format=json counterpart. Always
// succeeds once the tool name itself is recognized -- an empty
// Vendors slice for a tool with no install support yet is itself the
// correct, complete answer, not an error condition.
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

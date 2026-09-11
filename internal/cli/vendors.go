package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newVendorsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "vendors <tool>",
		Short:   "Show which vendors a tool can be installed from",
		Example: `  sk vendors java`,
		Args:    requireArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			toolName := args[0]

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

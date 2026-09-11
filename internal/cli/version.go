package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newVersionCmd replaces cobra's own auto-added -v/--version flag with
// a plain subcommand -- deliberate: this project's stated design
// principle is one clean way to do one thing, not a command AND a
// flag both reaching the same result. Removing cobra's Version field
// (see root.go) stops it from adding -v/--version at all; this is the
// only remaining way to ask sk for its own version.
func newVersionCmd(version string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show sk's own version",
		Args:  requireArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println(version)
			return nil
		},
	}
}

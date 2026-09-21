package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// versionPayload is version's --format=json success shape. Unlike
// every other command's payload, this has no `action` field -- it's
// a read-only report, not a state-changing operation, so it doesn't
// belong to the shared activationAction enum.
//
// Commit and GoVersion are both omitted (via omitempty) on a "dev"
// build (see main.go's own doc comment on the version var): commit
// is only meaningful when ldflags actually injected a real git SHA,
// and design doc §3's convention is that a local, unreleased build
// reports plainly rather than implying a precision it doesn't have.
type versionPayload struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	GoVersion string `json:"goVersion,omitempty"`
}

// newVersionCmd replaces cobra's own auto-added -v/--version flag with
// a plain subcommand -- deliberate: this project's stated design
// principle is one clean way to do one thing, not a command AND a
// flag both reaching the same result. Removing cobra's Version field
// (see root.go) stops it from adding -v/--version at all; this is the
// only remaining way to ask sk for its own version.
//
// commit is passed through the same way version is (see root.go's
// Execute doc comment) -- injected at build time via ldflags, empty
// for a local "dev" build.
func newVersionCmd(version, commit string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show sk's own version",
		Args:  requireArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			if outputFormat == FormatJSON {
				data := &versionPayload{Version: version}
				if version != "dev" {
					data.Commit = commit
					data.GoVersion = runtime.Version()
				}
				return emitJSON(data, nil)
			}
			fmt.Println(version)
			return nil
		},
	}
}

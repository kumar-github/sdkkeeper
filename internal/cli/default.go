package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newDefaultCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "default <tool> [version|null]",
		Short: "Set, show, or clear the remembered default version for a tool",
		Example: `  sk default java 21.0.2-temurin   # set
  sk default java                    # show current
  sk default java null                # clear`,
		Args: requireArgs(cobra.RangeArgs(1, 2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			toolName := args[0]

			tool, err := requireTool(toolName)
			if err != nil {
				return err
			}

			// No second argument -- show the current default,
			// whatever it is (including "none set").
			if len(args) == 1 {
				current, err := readDefault(tool)
				if err != nil {
					fmt.Fprintln(session.Out)
					fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf("\u2717 Could not read default: %s", err)))
					return err
				}
				fmt.Fprintln(session.Out)
				if current == "" {
					fmt.Fprintln(session.Out, styles.Neutral.Render(fmt.Sprintf("No default %s set.", tool.DisplayName)))
				} else {
					// Mirrors the SET confirmation's own reminder
					// wording exactly -- a real gap found via actual
					// use: this bare, query-only path dropped the
					// same "run `sk use ... default` in each new
					// shell to activate it" reminder the SET
					// confirmation deliberately includes, even though
					// it's answering the exact same "what's my
					// default" question and carries the exact same
					// risk of a user assuming a stored default is
					// already active.
					fmt.Fprintln(session.Out, styles.Neutral.Render(
						fmt.Sprintf("Default %s: %s — run `sk use %s default` in each new shell to activate it", tool.DisplayName, current, tool.Name),
					))
				}
				return nil
			}

			value := args[1]

			// "null" clears the default -- occupies the exact same
			// argument slot a real version would, matching the same
			// "special value stays in the normal value's position"
			// convention as "default" does for `use` (see resolveUse's
			// own comment on this). Never a real version identifier
			// could collide with this, since every real one always
			// carries a vendor suffix.
			if value == "null" {
				err := os.Remove(tool.DefaultPath())
				if err != nil && !os.IsNotExist(err) {
					fmt.Fprintln(session.Out)
					fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf("\u2717 Could not clear default: %s", err)))
					return err
				}
				fmt.Fprintln(session.Out)
				fmt.Fprintln(session.Out, styles.Success.Render(fmt.Sprintf("\u2713 Default %s cleared", tool.DisplayName)))
				return nil
			}

			// Setting a default is validated against what's actually
			// installed FIRST -- refusing a typo here, immediately,
			// rather than letting it silently succeed and only
			// surface much later when `sk use <tool> default` fails
			// on a version that was never real to begin with. Matches
			// how `add`/`install` already validate upfront rather
			// than defer failures.
			versionDir := filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+value)
			if _, err := os.Lstat(versionDir); err != nil {
				fmt.Fprintln(session.Out)
				fmt.Fprintln(session.Out, styles.Error.Render(
					fmt.Sprintf("\u2717 %s%s is not installed — cannot set it as default", tool.FolderPrefix, value),
				))
				return fmt.Errorf("not installed")
			}

			if err := os.MkdirAll(filepath.Dir(tool.DefaultPath()), 0o755); err != nil {
				return fmt.Errorf("could not create defaults directory: %w", err)
			}
			if err := os.WriteFile(tool.DefaultPath(), []byte(value), 0o644); err != nil {
				fmt.Fprintln(session.Out)
				fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf("\u2717 Could not set default: %s", err)))
				return err
			}

			fmt.Fprintln(session.Out)
			fmt.Fprintln(session.Out, styles.Success.Render(
				fmt.Sprintf("\u2713 Default %s set to %s — run `sk use %s default` in each new shell to activate it", tool.DisplayName, value, tool.Name),
			))
			return nil
		},
	}
}

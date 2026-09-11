package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <tool> <version> <path>",
		Short: "Register an existing install (not managed by SDK Keeper) under a version label",
		Example: `sk add java 21.0.2 ~/MyApps/JAVA/JDK-21.0.2
  sk add maven 3.9.14 ~/MyApps/APACHE-MAVEN/apache-maven-3.9.14`,
		Args: requireArgs(cobra.ExactArgs(3)),
		RunE: func(cmd *cobra.Command, args []string) error {
			toolName, version, sourcePath := args[0], args[1], args[2]

			tool, err := requireTool(toolName)
			if err != nil {
				return err
			}

			// Always resolve to an absolute path before creating the
			// symlink -- a relative path baked into a symlink is
			// resolved relative to the SYMLINK's own directory when
			// later followed, not the user's cwd at the time `add` was
			// run. Without this, a relative path argument would
			// silently point somewhere wrong.
			absSource, err := filepath.Abs(sourcePath)
			if err != nil {
				return fmt.Errorf("could not resolve path %q: %w", sourcePath, err)
			}

			info, err := os.Stat(absSource)
			if err != nil {
				fmt.Fprintln(session.Out)
				fmt.Fprintln(session.Out, styles.Error.Render(
					fmt.Sprintf("\u2717 %s does not exist or is not accessible", absSource),
				))
				return fmt.Errorf("source path not accessible: %w", err)
			}
			if !info.IsDir() {
				fmt.Fprintln(session.Out)
				fmt.Fprintln(session.Out, styles.Error.Render(
					fmt.Sprintf("\u2717 %s is not a directory", absSource),
				))
				return fmt.Errorf("source path is not a directory")
			}

			target := filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+version)

			// Lstat, not Stat: Stat follows symlinks and would report
			// "not found" for a target that's already a DANGLING
			// symlink, incorrectly allowing a second `add` to overwrite
			// it. Lstat checks the entry itself, correctly detecting
			// "something already occupies this path" whether it's a
			// real directory, a working symlink, or a broken one.
			if _, err := os.Lstat(target); err == nil {
				fmt.Fprintln(session.Out)
				fmt.Fprintln(session.Out, styles.Error.Render(
					fmt.Sprintf("\u2717 %s%s already exists at %s", tool.FolderPrefix, version, target),
				))
				return fmt.Errorf("%s already exists", target)
			}

			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("could not create %s: %w", filepath.Dir(target), err)
			}

			if err := os.Symlink(absSource, target); err != nil {
				return fmt.Errorf("could not create symlink at %s: %w", target, err)
			}

			fmt.Fprintln(session.Out)
			fmt.Fprintln(session.Out, styles.Success.Render(
				fmt.Sprintf("\u2713 %s %s added — run `sk use %s %s` to activate it", tool.DisplayName, version, tool.Name, version),
			))
			return nil
		},
	}
}

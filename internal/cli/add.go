package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"sdkkeeper/internal/tooldef"
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

			// --format=json is a separate, non-interactive path,
			// exactly like use/remove -- no session.Out, no
			// styled error text, just the JSON envelope.
			if outputFormat == FormatJSON {
				data, jerr := resolveAddJSON(toolName, version, sourcePath)
				return emitJSON(data, jerr)
			}

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

// addPayload is add's --format=json success shape: the shared
// {tool, version, vendor, action} fields plus Path -- the real
// location the new symlink was created at, so a caller doesn't need
// a second `sk list` call to confirm it.
type addPayload struct {
	Tool    string  `json:"tool"`
	Version string  `json:"version"`
	Vendor  *string `json:"vendor"`
	Action  string  `json:"action"`
	Path    string  `json:"path"`
}

// resolveAddJSON is add's --format=json counterpart: no session.Out,
// no styled text -- otherwise mirrors the interactive RunE's own
// validation and symlink-creation sequence exactly (absolute-path
// resolution, Stat/IsDir checks, Lstat-based already-exists check).
func resolveAddJSON(toolName, version, sourcePath string) (*addPayload, *jsonError) {
	tool, ok := tooldef.Get(toolName)
	if !ok {
		return nil, &jsonError{Code: ErrCodeAmbiguousTool, Message: fmt.Sprintf("unknown tool: %s", toolName)}
	}

	absSource, err := filepath.Abs(sourcePath)
	if err != nil {
		return nil, &jsonError{Code: ErrCodeInvalidPath, Message: fmt.Sprintf("could not resolve path %q: %s", sourcePath, err)}
	}

	info, err := os.Stat(absSource)
	if err != nil {
		return nil, &jsonError{Code: ErrCodeInvalidPath, Message: fmt.Sprintf("%s does not exist or is not accessible", absSource)}
	}
	if !info.IsDir() {
		return nil, &jsonError{Code: ErrCodeInvalidPath, Message: fmt.Sprintf("%s is not a directory", absSource)}
	}

	target := filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+version)

	// Lstat, not Stat -- see the interactive RunE's own comment: this
	// must also catch an already-occupied, dangling symlink target.
	if _, err := os.Lstat(target); err == nil {
		return nil, &jsonError{Code: ErrCodeAlreadyRegistered, Message: fmt.Sprintf("%s%s already exists at %s", tool.FolderPrefix, version, target)}
	}

	// The target is genuinely new at this point, so a failure from
	// here on is activation_failed (the state-changing operation
	// itself failing), not internal_error.
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, &jsonError{Code: ErrCodeActivationFailed, Message: fmt.Sprintf("could not create %s: %s", filepath.Dir(target), err)}
	}
	if err := os.Symlink(absSource, target); err != nil {
		return nil, &jsonError{Code: ErrCodeActivationFailed, Message: fmt.Sprintf("could not create symlink at %s: %s", target, err)}
	}

	return &addPayload{
		Tool:    tool.Name,
		Version: version,
		Vendor:  vendorOf(tool.Name, version),
		Action:  string(actionAdded),
		Path:    target,
	}, nil
}

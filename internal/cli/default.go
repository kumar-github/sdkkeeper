package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"sdkkeeper/internal/inventory"
	"sdkkeeper/internal/tooldef"
)

func newDefaultCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "default <tool> [version|null]",
		Short: "Set, show, or clear the remembered default version for a tool",
		Example: `  sk default java 21.0.2-temurin   # set
  sk default java                    # show current
  sk default java null               # clear`,
		Args: requireArgs(cobra.RangeArgs(1, 2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			toolName := args[0]

			versionArg := ""
			if len(args) == 2 {
				versionArg = args[1]
			}

			if outputFormat == FormatJSON {
				data, jerr := resolveDefaultJSON(toolName, versionArg)
				return emitJSON(data, jerr)
			}

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
					fmt.Fprintln(session.Out, styles.Neutral.Render(
						fmt.Sprintf("Default %s: %s — run `sk use %s default` in each new shell to activate it", tool.DisplayName, current, tool.Name),
					))
				}
				return nil
			}

			value := args[1]

			// "null" clears the default -- occupies the same
			// argument slot a real version would; no real version
			// identifier could collide, since every one carries a
			// vendor suffix.
			if value == "null" {
				err := clearDefaultFile(tool)
				if err != nil {
					fmt.Fprintln(session.Out)
					fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf("\u2717 Could not clear default: %s", err)))
					return err
				}
				fmt.Fprintln(session.Out)
				fmt.Fprintln(session.Out, styles.Success.Render(fmt.Sprintf("\u2713 Default %s cleared", tool.DisplayName)))
				return nil
			}

			// Validated against what's actually installed first,
			// refusing a typo immediately rather than deferring the
			// failure to a later `sk use <tool> default`.
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

// resolveDefaultJSON is default's --format=json counterpart, covering
// all three interactive shapes (show/set/clear) under the same
// activationPayload/action enum as use/remove.
func resolveDefaultJSON(toolName, versionArg string) (*activationPayload, *jsonError) {
	tool, ok := tooldef.Get(toolName)
	if !ok {
		return nil, &jsonError{Code: ErrCodeAmbiguousTool, Message: fmt.Sprintf("unknown tool: %s", toolName)}
	}

	if versionArg == "" {
		// A default that's currently set is "defaulted" whether this
		// call set it or is merely reporting one set earlier -- the
		// enum has no separate "show" value.
		current, err := readDefault(tool)
		if err != nil {
			return nil, &jsonError{Code: ErrCodeInternalError, Message: err.Error()}
		}
		if current == "" {
			return nil, &jsonError{Code: ErrCodeNotFound, Message: fmt.Sprintf("no default %s set", tool.DisplayName)}
		}
		return &activationPayload{
			Tool:    tool.Name,
			Version: current,
			Vendor:  vendorOf(tool.Name, current),
			Action:  string(actionDefaulted),
		}, nil
	}

	if versionArg == "null" {
		if err := clearDefaultFile(tool); err != nil {
			return nil, &jsonError{Code: ErrCodeActivationFailed, Message: fmt.Sprintf("could not clear default: %s", err)}
		}
		return &activationPayload{
			Tool:    tool.Name,
			Version: "",
			Vendor:  nil,
			Action:  string(actionDefaultCleared),
		}, nil
	}

	if _, ok := inventory.Find(tool, versionArg); !ok {
		return nil, &jsonError{Code: ErrCodeNotFound, Message: fmt.Sprintf("%s%s is not installed -- cannot set it as default", tool.FolderPrefix, versionArg)}
	}
	if err := os.MkdirAll(filepath.Dir(tool.DefaultPath()), 0o755); err != nil {
		return nil, &jsonError{Code: ErrCodeActivationFailed, Message: fmt.Sprintf("could not create defaults directory: %s", err)}
	}
	if err := os.WriteFile(tool.DefaultPath(), []byte(versionArg), 0o644); err != nil {
		return nil, &jsonError{Code: ErrCodeActivationFailed, Message: fmt.Sprintf("could not set default: %s", err)}
	}
	return &activationPayload{
		Tool:    tool.Name,
		Version: versionArg,
		Vendor:  vendorOf(tool.Name, versionArg),
		Action:  string(actionDefaulted),
	}, nil
}

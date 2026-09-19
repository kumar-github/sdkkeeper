package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"sdkkeeper/internal/inventory"
	"sdkkeeper/internal/picker"
	"sdkkeeper/internal/tooldef"
)

// removeVersion deletes v from disk using the correct mechanism for
// each case. A symlink (v.External, registered via `add`) has only
// the symlink itself removed -- the real files it points at belong to
// the user, not sk. A real, sk-installed directory is removed
// entirely. Extracted so this distinction can be tested directly.
func removeVersion(v inventory.Version) error {
	if v.External {
		return os.Remove(v.Path)
	}
	return os.RemoveAll(v.Path)
}

func newRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <tool> [version]",
		Short: "Remove an installed or registered version",
		Example: `  sk remove java 21.0.2-temurin   # exact version, no picker
  sk remove java                    # picker, matching 'use'`,
		Args: requireArgs(cobra.RangeArgs(1, 2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			toolName := args[0]

			version := ""
			if len(args) == 2 {
				version = args[1]
			}

			if outputFormat == FormatJSON {
				data, jerr := resolveRemoveJSON(toolName, version)
				return emitJSON(data, jerr)
			}

			tool, err := requireTool(toolName)
			if err != nil {
				return err
			}

			// No version given -- show a picker, matching `use`.
			if version == "" {
				versions, err := inventory.Scan(tool)
				if err != nil {
					return err
				}
				if len(versions) == 0 {
					fmt.Fprintln(session.Out)
					fmt.Fprintln(session.Out, styles.Neutral.Render(fmt.Sprintf("No %s versions found to remove.", tool.DisplayName)))
					return nil
				}
				if !session.HasTTY {
					err := versionRequiredNoTTY(tool, "remove")
					fmt.Fprintln(session.Out)
					fmt.Fprintln(session.Out, styles.Error.Render(err.Error()))
					return err
				}
				title := fmt.Sprintf("Select %s version to remove", tool.DisplayName)
				chosen, err := picker.RunGrouped(session, title, buildPickerGroups(tool, versions), "")
				if err != nil {
					fmt.Fprintln(session.Out)
					fmt.Fprintln(session.Out, styles.Error.Render(err.Error()))
					return err
				}
				if chosen == "" {
					fmt.Fprintln(session.Out)
					fmt.Fprintln(session.Out, styles.Neutral.Render(fmt.Sprintf("No %s version selected to remove.", tool.DisplayName)))
					return ErrCancelled
				}
				version = chosen
			}

			v, ok := inventory.Find(tool, version)
			if !ok {
				versions, _ := inventory.Scan(tool)
				msg := inventory.FormatNotFound(tool.FolderPrefix, version, "remove", tool.DisplayName, versions)
				fmt.Fprintln(session.Out)
				fmt.Fprintln(session.Out, styles.Error.Render(msg))
				return fmt.Errorf("not found")
			}

			// Checked before deletion: HomePath/BinPath probe the
			// filesystem, so computing them after the directory is
			// gone would give a different, wrong answer.
			wasActive := false
			var activeBinPath string
			if tool.EnvVar != "" {
				if current, isSet := os.LookupEnv(tool.EnvVar); isSet {
					_, wasActive = findActiveVersion(tool, []inventory.Version{v}, current)
					if wasActive {
						activeBinPath = tool.BinPath(v.Path)
					}
				}
			}

			removeErr := removeVersion(v)
			if removeErr != nil {
				fmt.Fprintln(session.Out)
				fmt.Fprintln(session.Out, styles.Error.Render(
					fmt.Sprintf("\u2717 Could not remove %s%s: %s", tool.FolderPrefix, version, removeErr),
				))
				return removeErr
			}

			// If the removed version was the stored default, clear
			// that too, so a later `sk use <tool> default` doesn't
			// fail with a confusing "not found" instead of a clear one.
			clearedDefault := false
			if stored, err := readDefault(tool); err == nil && stored == version {
				if err := clearDefaultFile(tool); err == nil {
					clearedDefault = true
				}
			}

			fmt.Fprintln(session.Out)
			if v.External {
				fmt.Fprintln(session.Out, styles.Success.Render(
					fmt.Sprintf("\u2713 %s %s unregistered (not managed by SDK Keeper — the real files were left untouched)", tool.DisplayName, version),
				))
			} else {
				fmt.Fprintln(session.Out, styles.Success.Render(
					fmt.Sprintf("\u2713 %s %s removed", tool.DisplayName, version),
				))
			}
			if clearedDefault {
				fmt.Fprintln(session.Out, styles.Detail.Render(
					fmt.Sprintf("  \u2514\u2500 was the default %s — default cleared", tool.DisplayName),
				))
			}
			if wasActive {
				fmt.Fprintln(session.Out, styles.Detail.Render(
					fmt.Sprintf("  \u2514\u2500 was the current %s — current cleared", tool.DisplayName),
				))
				// Clearing the env var alone isn't enough: PATH may
				// still have this version's bin directory prepended,
				// so writeDeactivate strips it too.
				writeDeactivate(tool.EnvVar, activeBinPath, parseShellFormat(shellFormatFlag))
				fmt.Fprintln(session.Out, styles.Detail.Render(
					fmt.Sprintf("  \u2514\u2500 %s cleared for this shell — no %s currently active", tool.EnvVar, tool.DisplayName),
				))
			}
			return nil
		},
	}
}

// removalPayload is remove's --format=json success shape: the shared
// {tool, version, vendor, action} fields plus WasCurrent, WasDefault,
// EnvVar, and BinPath.
//
// --format=json is a report, never a shell action -- it can no more
// unset the caller's JAVA_HOME than `use` can set it (see
// resolveUseJSON). WasCurrent/WasDefault tell a caller whether the
// version it just removed was the one its own shell was pointing at;
// EnvVar/BinPath tell it exactly what to unset and strip from PATH to
// clean up correctly:
//
//	if wasCurrent: unset $envVar; strip $binPath from PATH
type removalPayload struct {
	Tool       string  `json:"tool"`
	Version    string  `json:"version"`
	Vendor     *string `json:"vendor"`
	Action     string  `json:"action"`
	WasCurrent bool    `json:"wasCurrent"`
	WasDefault bool    `json:"wasDefault"`
	EnvVar     string  `json:"envVar"`
	BinPath    string  `json:"binPath"`
}

// resolveRemoveJSON is remove's --format=json counterpart: no picker,
// no session.Out. Mirrors the interactive RunE (find, check
// wasCurrent/wasDefault, delete, clear a matching stored default).
func resolveRemoveJSON(toolName, versionArg string) (*removalPayload, *jsonError) {
	tool, ok := tooldef.Get(toolName)
	if !ok {
		return nil, &jsonError{Code: ErrCodeAmbiguousTool, Message: fmt.Sprintf("unknown tool: %s", toolName)}
	}

	if versionArg == "" {
		return nil, &jsonError{Code: ErrCodeVersionRequired, Message: fmt.Sprintf("a version is required to remove %s in --format=json (the interactive picker cannot be shown)", tool.DisplayName)}
	}

	v, ok := inventory.Find(tool, versionArg)
	if !ok {
		return nil, &jsonError{Code: ErrCodeNotFound, Message: fmt.Sprintf("%s%s not found", tool.FolderPrefix, versionArg)}
	}

	// Checked before deletion -- HomePath/BinPath need the real
	// files to still exist.
	wasCurrent := false
	if tool.EnvVar != "" {
		if current, isSet := os.LookupEnv(tool.EnvVar); isSet {
			_, wasCurrent = findActiveVersion(tool, []inventory.Version{v}, current)
		}
	}
	wasDefault := false
	if stored, err := readDefault(tool); err == nil && stored == v.Number {
		wasDefault = true
	}
	binPath := tool.BinPath(v.Path)

	// The target was genuinely resolved, so a failure from here is
	// activation_failed, not internal_error.
	if err := removeVersion(v); err != nil {
		return nil, &jsonError{Code: ErrCodeActivationFailed, Message: fmt.Sprintf("could not remove %s%s: %s", tool.FolderPrefix, v.Number, err)}
	}

	// Best-effort, like the interactive path: a failure here doesn't
	// undo the deletion or become this call's own error.
	if wasDefault {
		_ = clearDefaultFile(tool)
	}

	return &removalPayload{
		Tool:       tool.Name,
		Version:    v.Number,
		Vendor:     vendorOf(tool.Name, v.Number),
		Action:     string(actionRemoved),
		WasCurrent: wasCurrent,
		WasDefault: wasDefault,
		EnvVar:     tool.EnvVar,
		BinPath:    binPath,
	}, nil
}

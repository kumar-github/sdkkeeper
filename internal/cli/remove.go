package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"sdkkeeper/internal/inventory"
	"sdkkeeper/internal/picker"
	"sdkkeeper/internal/tooldef"
)

// removeVersion deletes v from disk, using the correct mechanism for
// each case -- the one thing this command must never get backwards.
// For a symlink (v.External, registered via `add`), only the symlink
// itself is removed (os.Remove) -- the real files it points at belong
// to the user, not sk, and must never be touched. For a real,
// genuinely sk-installed directory, the actual files are removed
// (os.RemoveAll). Extracted as its own function specifically so this
// critical distinction can be tested directly, without needing a real
// terminal session.
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

			// --format=json branches off before the interactive tool
			// lookup/picker/session.Out machinery below -- same
			// reasoning as use.go's own branch (design doc §2/§6):
			// never launches a picker, reports version_required
			// immediately instead when no version is given.
			if outputFormat == FormatJSON {
				data, jerr := resolveRemoveJSON(toolName, version)
				return emitJSON(data, jerr)
			}

			tool, err := requireTool(toolName)
			if err != nil {
				return err
			}

			// No version given -- show a picker, matching `use`'s own
			// established "no arg = show a picker" pattern. A real
			// bug caught via actual use: this command used to require
			// EXACTLY 2 args, so `sk remove java` with no version
			// failed cobra's own arg-count validation and, since
			// root.go silences errors, exited with no output
			// whatsoever -- a genuinely confusing silent no-op rather
			// than either removing something or explaining why not.
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
				title := fmt.Sprintf("Select %s version to remove", tool.DisplayName)
				chosen, err := picker.RunGrouped(session, title, buildPickerGroups(tool, versions), "")
				if err != nil {
					fmt.Fprintln(session.Out)
					fmt.Fprintln(session.Out, styles.Error.Render(err.Error()))
					return err
				}
				if chosen == "" {
					fmt.Fprintln(session.Out)
					// Styled Detail (neutral), not Error -- a picker
					// being cancelled is the user's own deliberate
					// choice, not a failure; see Styles' own doc
					// comment for the full convention.
					fmt.Fprintln(session.Out, styles.Neutral.Render(fmt.Sprintf("No %s version selected to remove.", tool.DisplayName)))
					return ErrCancelled
				}
				version = chosen
			}

			// Reuses the same inventory.Find/FormatNotFound already
			// proven by `use`'s own not-found path -- v.External tells
			// us directly whether this is a real, sk-installed
			// directory or a symlink (registered via `add`), no need
			// to re-derive that here.
			v, ok := inventory.Find(tool, version)
			if !ok {
				versions, _ := inventory.Scan(tool)
				msg := inventory.FormatNotFound(tool.FolderPrefix, version, "remove", tool.DisplayName, versions)
				fmt.Fprintln(session.Out)
				fmt.Fprintln(session.Out, styles.Error.Render(msg))
				return fmt.Errorf("not found")
			}

			// Checked BEFORE deletion, deliberately -- with the
			// HomePath filesystem-probing fix (see tooldef.Tool's own
			// doc comment), calling it AFTER the directory is gone
			// would give a different, wrong answer (the probe would
			// no longer find "Contents/Home" even if it existed
			// before deletion), so this must capture the comparison
			// while the real files still exist. Reuses
			// findActiveVersion directly (already built and tested
			// for `current`) rather than duplicating its comparison
			// logic here.
			wasActive := false
			var activeBinPath string
			if tool.EnvVar != "" {
				if current, isSet := os.LookupEnv(tool.EnvVar); isSet {
					_, wasActive = findActiveVersion(tool, []inventory.Version{v}, current)
					if wasActive {
						// Captured HERE, before deletion, for the exact
						// same reason as wasActive itself: BinPath now
						// probes the filesystem (see tooldef.Tool's own
						// doc comment) -- computing it AFTER v.Path no
						// longer exists would give a different, wrong
						// answer than what was actually prepended to
						// PATH when this version was originally
						// activated.
						activeBinPath = tool.BinPath(v.Path)
					}
				}
			}

			// The one thing this command must never get backwards --
			// see removeVersion's own doc comment.
			removeErr := removeVersion(v)
			if removeErr != nil {
				fmt.Fprintln(session.Out)
				fmt.Fprintln(session.Out, styles.Error.Render(
					fmt.Sprintf("\u2717 Could not remove %s%s: %s", tool.FolderPrefix, version, removeErr),
				))
				return removeErr
			}

			// If the removed version was the currently-set default,
			// clear that too -- otherwise `sk default <tool>` would
			// keep pointing at something that no longer exists, and
			// `sk use <tool> default` would later fail with a
			// confusing "not found" error instead of a clear one.
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
				// "was the current %s — current cleared" mirrors the
				// clearedDefault line's own shape exactly -- a real
				// gap found via actual use: removing the DEFAULT
				// version announced its role before describing the
				// side effect ("was the default X -- default
				// cleared"), but removing the ACTIVE version jumped
				// straight to the side effect (JAVA_HOME cleared)
				// with no equivalent role announcement at all.
				fmt.Fprintln(session.Out, styles.Detail.Render(
					fmt.Sprintf("  \u2514\u2500 was the current %s — current cleared", tool.DisplayName),
				))
				// `remove` is now eval-cooperating for exactly this
				// one, narrow case -- see writeDeactivate's own doc
				// comment for the full reasoning, including a real
				// gap found via actual use: clearing the env var
				// alone wasn't enough, since PATH still had this
				// version's bin directory prepended too, and `sk
				// use` never removes an EARLIER prepend when a
				// different version is activated later in the same
				// session -- so PATH could still resolve to some
				// other, unexpected version even with the env var
				// genuinely gone. wasActive is the SAME check that
				// already powered the old, tell-the-user-to-fix-it-
				// themselves message; this doesn't loosen that
				// condition at all, it just adds a real, complete fix
				// on top of it, only when it's already true.
				writeDeactivate(tool.EnvVar, activeBinPath, parseShellFormat(shellFormatFlag))
				fmt.Fprintln(session.Out, styles.Detail.Render(
					fmt.Sprintf("  \u2514\u2500 %s cleared for this shell — no %s currently active", tool.EnvVar, tool.DisplayName),
				))
			}
			return nil
		},
	}
}

// removalPayload is remove's OWN success `data` shape -- built on the
// same {tool, version, vendor, action} fields design doc §2 gives
// use/default (which keep that exact, unmodified shape -- see
// activationPayload), plus two additive fields the frozen doc doesn't
// cover: WasCurrent and WasDefault.
//
// A real, confirmed gap found via actual use, not a hypothetical:
// removing the version currently active in the CALLING shell deletes
// the real files on disk exactly like interactive `sk remove` does,
// but --format=json is (by design -- see resolveUseJSON's own doc
// comment on the identical limitation for `use`) a REPORT, never a
// shell action: it can no more unset the caller's JAVA_HOME than it
// can set it. Without these two fields, a caller had ZERO way to
// learn that the version it just told sk to delete was the one its
// OWN environment was still pointing at -- exactly what happened:
// `java` broke immediately after the remove call, and a SEPARATE,
// later `sk current java` could only report a raw, unmatched
// JAVA_HOME value, because nothing had ever told that shell to clear
// it. These two fields don't fix that inherent limitation -- nothing
// can, short of the caller reacting to them by unsetting the relevant
// env var itself -- but they make the situation visible instead of
// silent.
type removalPayload struct {
	Tool       string  `json:"tool"`
	Version    string  `json:"version"`
	Vendor     *string `json:"vendor"`
	Action     string  `json:"action"`
	WasCurrent bool    `json:"wasCurrent"`
	WasDefault bool    `json:"wasDefault"`
}

// resolveRemoveJSON is `remove`'s --format=json counterpart -- no
// picker, no session.Out; mirrors the interactive RunE's own logic
// (find the version, check wasCurrent/wasDefault, delete it, clear a
// matching stored default) exactly, minus every piece of console
// output and picker fallback.
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

	// Both checked BEFORE deletion, deliberately -- mirrors the
	// interactive RunE's own wasActive check exactly, including WHY it
	// has to happen first: HomePath's filesystem probing (see
	// tooldef.Tool's own doc comment) needs the real files to still
	// exist to give the right answer; computing this AFTER v.Path is
	// gone would silently give a different, wrong result.
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

	// The target was genuinely resolved -- a failure from here on is
	// activation_failed (design doc: "resolved version, but activation
	// itself failed"), generalized here to "the state-changing
	// operation on the resolved target failed", not internal_error.
	if err := removeVersion(v); err != nil {
		return nil, &jsonError{Code: ErrCodeActivationFailed, Message: fmt.Sprintf("could not remove %s%s: %s", tool.FolderPrefix, v.Number, err)}
	}

	// Best-effort, exactly like the interactive path: a failure here
	// doesn't undo the deletion that already succeeded, and isn't
	// itself reported as this call's own error -- matching
	// clearedDefault's own "clearedDefault := false" swallow-and-move-on
	// pattern in the interactive RunE above. wasDefault is already
	// known from the check above, so this only needs to ACT on it now.
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
	}, nil
}

package cli

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"sdkkeeper/internal/inventory"
	"sdkkeeper/internal/registry"
	"sdkkeeper/internal/tooldef"
)

func newSearchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "search <tool> <vendor> [major]",
		Short: "Show versions available to install from a vendor's remote catalog (not yet installed)",
		Example: `  sk search java temurin      # major versions only
  sk search java temurin 21   # every patch within major 21`,
		Args: requireArgs(cobra.RangeArgs(2, 3)),
		RunE: func(cmd *cobra.Command, args []string) error {
			toolName, vendorName := args[0], args[1]

			// --format=json branches off before requireTool's own
			// session.Out printing -- both the majors and patches
			// shapes below need the same tool/vendor validation the
			// interactive path does, just reported as a JSON error
			// instead of printed text.
			if outputFormat == FormatJSON {
				major := ""
				if len(args) == 3 {
					major = args[2]
				}
				data, jerr := buildSearchJSON(cmd.Context(), toolName, vendorName, major)
				return emitJSON(data, jerr)
			}

			tool, err := requireTool(toolName)
			if err != nil {
				return err
			}

			provider, ok := providersFor(tool.Name)[vendorName]
			if !ok {
				available := vendorNamesFor(tool.Name)
				displayNames := make([]string, len(available))
				for i, name := range available {
					displayNames[i] = capitalize(name)
				}
				fmt.Fprintln(session.Out)
				fmt.Fprintln(session.Out, styles.Error.Render(
					fmt.Sprintf("\u2717 unknown vendor: %s (available: %s)", vendorName, strings.Join(displayNames, ", ")),
				))
				return fmt.Errorf("unknown vendor: %s", vendorName)
			}

			ctx := cmd.Context()
			header := capitalize(provider.Name())

			if len(args) == 2 {
				return searchMajors(ctx, tool, provider, header)
			}
			return searchPatches(ctx, tool, provider, header, args[2])
		},
	}
}

// searchMajors prints every major/feature version a vendor currently
// offers, with LTS releases flagged -- both Temurin's and Liberica's
// APIs already report this directly, no extra network call beyond
// what listing majors already makes.
func searchMajors(ctx context.Context, tool tooldef.Tool, provider registry.Provider, header string) error {
	infos, err := provider.ListMajorVersionsWithLTS(ctx)
	if err != nil {
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Error.Render(
			fmt.Sprintf("\u2717 Could not list available %s major versions: %s", tool.DisplayName, err),
		))
		return err
	}

	fmt.Fprintln(session.Out)
	fmt.Fprintln(session.Out, styles.Header.Render(header+":"))
	rows := make([][]string, len(infos))
	for i, info := range infos {
		ltsLabel := ""
		if info.LTS {
			ltsLabel = "LTS"
		}
		rows[i] = []string{info.Number, ltsLabel}
	}
	fmt.Fprint(session.Out, indentLines(renderTable(rows, 2, tableWidth(rows, 2), nil), "  "))
	return nil
}

// searchPatches prints every patch version within one major, marking
// whichever one (if any) is already installed or registered locally --
// a single marker only (see design discussion: no separate "currently
// active" or "default" indicator, to avoid crowding).
func searchPatches(ctx context.Context, tool tooldef.Tool, provider registry.Provider, header, major string) error {
	patches, err := provider.ListPatchVersions(ctx, major, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		if err == registry.ErrVersionNotFound {
			fmt.Fprintln(session.Out)
			fmt.Fprintln(session.Out, styles.Error.Render(
				fmt.Sprintf("\u2717 No %s %s releases found for %s/%s via %s", tool.DisplayName, major, runtime.GOOS, runtime.GOARCH, provider.Name()),
			))
			return fmt.Errorf("no releases found")
		}
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Error.Render(
			fmt.Sprintf("\u2717 Could not list %s %s versions: %s", tool.DisplayName, major, err),
		))
		return err
	}

	// installedFor maps a bare patch (e.g. "21.0.2") to the matching
	// local inventory entry, if any -- checking BOTH the current,
	// vendor-suffixed naming convention (e.g. "21.0.2-temurin") AND a
	// bare match with no suffix at all, for backward compatibility
	// with installs that predate vendor-suffixed naming.
	installed, _ := inventory.Scan(tool)
	installedFor := make(map[string]inventory.Version, len(installed))
	for _, v := range installed {
		installedFor[v.Number] = v
	}

	fmt.Fprintln(session.Out)
	fmt.Fprintln(session.Out, styles.Header.Render(fmt.Sprintf("%s %s:", header, major)))
	rows := make([][]string, len(patches))
	for i, p := range patches {
		marker := ""
		if v, ok := installedFor[p+"-"+provider.Name()]; ok {
			marker = installedMarker(v)
		} else if v, ok := installedFor[p]; ok {
			marker = installedMarker(v)
		}
		rows[i] = []string{p, marker}
	}
	fmt.Fprint(session.Out, indentLines(renderTable(rows, 2, tableWidth(rows, 2), nil), "  "))
	return nil
}

// installedMarker builds the single "already have this" indicator --
// distinguishing a real, sk-installed entry from a symlinked
// (registered via `add`) one, reusing the exact phrase already
// established in `list`'s own output for that same distinction.
func installedMarker(v inventory.Version) string {
	if v.External {
		return "\u2713 installed (not managed by SDK Keeper)"
	}
	return "\u2713 installed"
}

// capitalize uppercases just the first letter, e.g. "temurin" ->
// "Temurin" -- for display headers only; provider.Name() itself stays
// lowercase everywhere else (command arguments, version-folder
// suffixes), matching the rest of this tool's naming conventions.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// searchEntry and searchData are `search`'s --format=json success
// `data` shape (design doc §3). LTS is a pointer, present only for
// the major-listing mode (no major arg given) -- design doc §3's own
// sample only covers the PATCH-listing mode ("available":
// [{version, isInstalled}]); it doesn't address search's other real
// mode (major versions, each with an LTS flag, no installed concept)
// at all. Rather than force majors into the exact same
// {version,isInstalled} shape and silently drop LTS entirely, this
// adds one omitempty field for that mode -- additive, not a
// contradiction of what the doc DOES specify.
type searchEntry struct {
	Version     string `json:"version"`
	IsInstalled bool   `json:"isInstalled"`
	LTS         *bool  `json:"lts,omitempty"`
}

type searchData struct {
	Tool      string        `json:"tool"`
	Vendor    string        `json:"vendor"`
	Available []searchEntry `json:"available"`
}

// buildSearchJSON is `search`'s --format=json counterpart --
// validates tool/vendor exactly like the interactive path, then
// dispatches to the majors or patches shape depending on whether a
// major was given, matching the interactive RunE's own len(args)
// branch.
func buildSearchJSON(ctx context.Context, toolName, vendorName, major string) (*searchData, *jsonError) {
	tool, ok := tooldef.Get(toolName)
	if !ok {
		return nil, &jsonError{Code: ErrCodeAmbiguousTool, Message: fmt.Sprintf("unknown tool: %s", toolName)}
	}

	provider, ok := providersFor(tool.Name)[vendorName]
	if !ok {
		available := vendorNamesFor(tool.Name)
		return nil, &jsonError{
			Code:    ErrCodeNotFound,
			Message: fmt.Sprintf("unknown vendor: %s", vendorName),
			Extra:   map[string]interface{}{"candidates": available},
		}
	}

	if major == "" {
		return buildSearchMajorsJSON(ctx, tool, provider)
	}
	return buildSearchPatchesJSON(ctx, tool, provider, major)
}

func buildSearchMajorsJSON(ctx context.Context, tool tooldef.Tool, provider registry.Provider) (*searchData, *jsonError) {
	infos, err := provider.ListMajorVersionsWithLTS(ctx)
	if err != nil {
		return nil, &jsonError{Code: ErrCodeInternalError, Message: fmt.Sprintf("could not list available %s major versions: %s", tool.DisplayName, err)}
	}

	entries := make([]searchEntry, len(infos))
	for i, info := range infos {
		lts := info.LTS
		entries[i] = searchEntry{Version: info.Number, LTS: &lts}
	}
	return &searchData{Tool: tool.Name, Vendor: provider.Name(), Available: entries}, nil
}

func buildSearchPatchesJSON(ctx context.Context, tool tooldef.Tool, provider registry.Provider, major string) (*searchData, *jsonError) {
	patches, err := provider.ListPatchVersions(ctx, major, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		if err == registry.ErrVersionNotFound {
			return nil, &jsonError{Code: ErrCodeNotFound, Message: fmt.Sprintf("no %s %s releases found for %s/%s via %s", tool.DisplayName, major, runtime.GOOS, runtime.GOARCH, provider.Name())}
		}
		return nil, &jsonError{Code: ErrCodeInternalError, Message: fmt.Sprintf("could not list %s %s versions: %s", tool.DisplayName, major, err)}
	}

	// Same dual bare/vendor-suffixed lookup as searchPatches' own text
	// rendering (see that function's doc comment for why both forms
	// are checked) -- kept identical here so the JSON and text paths
	// can never disagree about which patches are already installed.
	installed, _ := inventory.Scan(tool)
	installedFor := make(map[string]inventory.Version, len(installed))
	for _, v := range installed {
		installedFor[v.Number] = v
	}

	entries := make([]searchEntry, len(patches))
	for i, p := range patches {
		_, isInstalled := installedFor[p+"-"+provider.Name()]
		if !isInstalled {
			_, isInstalled = installedFor[p]
		}
		entries[i] = searchEntry{Version: p, IsInstalled: isInstalled}
	}
	return &searchData{Tool: tool.Name, Vendor: provider.Name(), Available: entries}, nil
}

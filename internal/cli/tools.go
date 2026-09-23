package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"sdkkeeper/internal/tooldef"
)

// toolNames returns every tool sdkkeeper knows how to manage
// (tooldef.Registry), alphabetically -- there's no existing
// vendor-order-style precedent across TOOLS themselves the way there
// is among a single tool's vendors (see vendorNamesFor's own doc
// comment), so alphabetical is the right default here, same as
// installSupportedTools already does for its own (narrower) subset.
func toolNames() []string {
	names := make([]string, 0, len(tooldef.Registry))
	for name := range tooldef.Registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// vendorsCell renders a tool's vendor list for the plain-text table:
// capitalized, comma-joined vendor names (matching `sk vendors`'
// own display convention), or "-" for a tool with no registered
// install provider yet (e.g. node, kafka, per providersByTool's own
// current contents) -- a plain fact, not an error, same as `sk
// vendors <tool>` reports it.
func vendorsCell(toolName string) string {
	names := vendorNamesFor(toolName)
	if len(names) == 0 {
		return "-"
	}
	display := make([]string, len(names))
	for i, n := range names {
		display[i] = capitalize(n)
	}
	return strings.Join(display, ", ")
}

func newToolsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "tools",
		Short:   "List every tool SDK Keeper knows how to manage",
		Example: `sk tools   # see 'sk install <tool>' or 'sk search <tool> <vendor>' next`,
		Args:    requireArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			if outputFormat == FormatJSON {
				return emitJSON(buildToolsJSON(), nil)
			}

			names := toolNames()
			rows := make([][]string, len(names))
			for i, name := range names {
				rows[i] = []string{name, vendorsCell(name)}
			}

			fmt.Fprintln(session.Out)
			fmt.Fprintln(session.Out, styles.Header.Render("Supported Tools"))
			fmt.Fprintln(session.Out)
			fmt.Fprint(session.Out, indentLines(
				renderBorderedTable([]string{"TOOL", "VENDORS"}, rows, styles.Header),
				"  ",
			))
			return nil
		},
	}
}

// toolEntry/toolsData are tools' --format=json success shape --
// static registry data only (name + vendors). Deliberately no
// installed/default/current fields: that's per-user install state,
// already owned by list/current/default respectively, and duplicating
// it here would just be a second place for it to drift out of sync --
// exactly the kind of cross-command consistency risk the design doc's
// own §3 nuance (list's isCurrent vs. current's active) flags as
// something to avoid multiplying, not add to. `sk tools` answers a
// different, static question: what sk's code knows how to manage at
// all, independent of any install state.
type toolEntry struct {
	Name    string   `json:"name"`
	Vendors []string `json:"vendors"`
}

type toolsData struct {
	Tools []toolEntry `json:"tools"`
}

// buildToolsJSON always succeeds -- tools is a static, registry-level
// listing with no per-argument input to validate, unlike every other
// discovery command.
func buildToolsJSON() *toolsData {
	names := toolNames()
	entries := make([]toolEntry, len(names))
	for i, name := range names {
		vendors := vendorNamesFor(name)
		if vendors == nil {
			vendors = []string{}
		}
		entries[i] = toolEntry{Name: name, Vendors: vendors}
	}
	return &toolsData{Tools: entries}
}

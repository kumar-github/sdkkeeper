package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"sdkkeeper/internal/inventory"
	"sdkkeeper/internal/picker"
	"sdkkeeper/internal/tooldef"
)

func newListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list [<tool>]",
		Short: "Show installed versions of a tool, or every tool",
		Example: `  sk list java           # just java
  sk list                # every registered tool, one section each
  sk list --sizes        # same, plus real on-disk size per version`,
		Args: requireArgs(cobra.RangeArgs(0, 1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Not computed unless asked: real disk usage means
			// walking every file in every installed version's own
			// directory tree (a JDK is hundreds of files) -- a
			// materially heavier operation than list's own normal,
			// cheap os.ReadDir-only scan. Making it opt-in keeps the
			// common "just show me what's installed" case exactly as
			// fast as it's always been.
			showSizes, _ := cmd.Flags().GetBool("sizes")

			// No tool name given -- show every registered tool, one
			// section each, rather than requiring N separate calls.
			// Deliberately NOT a --all-tools flag: sk's own vocabulary
			// treats a distinct target/scope as a positional word (or,
			// as here, the ABSENCE of one meaning "every tool" -- the
			// same pattern `sk use` already uses for its own
			// zero-arg .skrc-batch case), never a flag for that.
			if len(args) == 0 {
				if outputFormat == FormatJSON {
					return emitJSON(buildAllToolsListJSON(showSizes), nil)
				}
				return printAllToolsList(showSizes)
			}
			toolName := args[0]

			// --format=json is a pure, read-only report.
			if outputFormat == FormatJSON {
				data, jerr := buildListJSON(toolName, showSizes)
				return emitJSON(data, jerr)
			}

			tool, err := requireTool(toolName)
			if err != nil {
				return err
			}

			versions, err := inventory.Scan(tool)
			if err != nil {
				return err
			}

			fmt.Println()
			printToolListBody(tool, versions, showSizes)
			return nil
		},
	}
	cmd.Flags().Bool("sizes", false, "show real on-disk size per installed version, plus subtotals")
	return cmd
}

// printToolListBody renders one tool's installed versions -- exactly
// today's single-tool `sk list <tool>` body, extracted so
// printAllToolsList can call it once per tool without duplicating any
// of this logic. Deliberately prints no leading blank line of its
// own -- the caller controls spacing, since that differs between the
// single-tool case (one blank line before this) and the all-tools
// case (a tool-name header immediately before this, no extra blank
// between them). Returns the tool's own total size in bytes (0 if
// showSizes is false) so printAllToolsList can accumulate a grand
// total across tools without re-walking anything.
func printToolListBody(tool tooldef.Tool, versions []inventory.Version, showSizes bool) int64 {
	// Determined ONCE, tool-wide -- current/default are facts about
	// the TOOL, not about which group (managed vs not) an entry
	// happens to fall into.
	var currentVersion string
	if tool.EnvVar != "" {
		if envVal, isSet := os.LookupEnv(tool.EnvVar); isSet {
			if v, ok := findActiveVersion(tool, versions, envVal); ok {
				currentVersion = v.Number
			}
		}
	}
	defaultVersion, _ := readDefault(tool)

	// Computed ONCE per version here, not inside annotate (which can
	// be called more than once per version across the managed/
	// external split below) -- avoids walking the same directory
	// tree twice.
	var sizes map[string]int64
	var toolTotal int64
	if showSizes {
		sizes = make(map[string]int64, len(versions))
		for _, v := range versions {
			n := dirSize(v.Path)
			sizes[v.Number] = n
			toolTotal += n
		}
	}

	// Returns the PLAIN (unstyled) current/default/size tag text for
	// a version -- coloring happens later, via renderTable's
	// styleFunc, not baked into these strings. A real, confirmed
	// reason for that split: lipgloss/table miscalculates column
	// widths when cell content carries embedded ANSI codes (verified
	// directly -- it silently truncated real version numbers when a
	// neighboring cell was pre-styled), so styling must stay separate
	// from the text itself, applied only at render time.
	annotate := func(v inventory.Version) (currentTag, defaultTag, sizeTag string) {
		if v.Number == currentVersion {
			currentTag = "(current)"
		}
		if v.Number == defaultVersion {
			defaultTag = "(default)"
		}
		if showSizes {
			sizeTag = formatMB(sizes[v.Number])
		}
		return currentTag, defaultTag, sizeTag
	}

	// Grouped by managed/not-managed with a header stated ONCE per
	// group, rather than repeating the full phrase on every single
	// line -- an earlier version of this did the latter and became a
	// genuinely crowded wall of text once more than a couple of
	// entries were present. Each group's versions stay in their
	// existing sort order (already newest-first from
	// inventory.Scan); an empty group's header is simply omitted
	// rather than printed with nothing underneath it.
	var managed, external []inventory.Version
	for _, v := range versions {
		if v.External {
			external = append(external, v)
		} else {
			managed = append(managed, v)
		}
	}

	// A real bug caught via actual use: with nothing installed OR
	// added, this printed absolutely nothing at all -- looking
	// exactly like the command had silently failed or hung, rather
	// than clearly communicating "there's nothing here yet".
	if len(managed) == 0 && len(external) == 0 {
		fmt.Println(styles.Neutral.Render(fmt.Sprintf("No %s versions found to list.", tool.DisplayName)))
		return 0
	}

	printedManaged := printManagedGroup(tool, styles.Header.Render("Managed by SDK Keeper:"), managed, annotate)
	if printedManaged && len(external) > 0 {
		fmt.Println()
	}
	// Not managed entries are deliberately NEVER grouped by vendor,
	// even when a label happens to look like a known vendor suffix
	// (e.g. someone `add`-registered an external JDK as
	// "17.0.3-temurin") -- that label is arbitrary user-typed text
	// with no verified relationship to the actual JDK at that path
	// (using the label "temurin" on a Liberica JDK doesn't make it
	// Temurin). Grouping by it would present unverified input as if
	// it were a confirmed fact, unlike the managed section above,
	// where the suffix is data sk itself generated during a real,
	// verified install.
	printGroup(styles.Header.Render("Not managed by SDK Keeper:"), external, annotate, "  ")

	if showSizes {
		fmt.Println()
		fmt.Println(styles.Detail.Render(fmt.Sprintf("  %s total: %s", tool.DisplayName, formatMB(toolTotal))))
	}
	return toolTotal
}

// printAllToolsList implements `sk list` with no arguments: one
// section per tool that actually has something installed, in the
// same deliberate presentation order doctor already uses (sortedTools
// -- Java, then Maven/Gradle, which both require it).
//
// Tools with NOTHING installed are skipped entirely here -- unlike an
// earlier version of this, which showed every registered tool
// including empty ones. That reasoning didn't hold up: `list`'s own
// job is showing what's INSTALLED, and `sk tools` already exists
// specifically to answer "what does sk support" regardless of
// install state. Duplicating that here, and doing it worse as the
// registry grows past a handful of tools (a wall of "No X versions
// found), served no one. --format=json filters the same way, for the
// same reason -- see buildAllToolsListJSON's own doc comment.
func printAllToolsList(showSizes bool) error {
	type populated struct {
		tool     tooldef.Tool
		versions []inventory.Version
	}
	var withInstalls []populated
	for _, tool := range sortedTools() {
		versions, err := inventory.Scan(tool)
		if err != nil {
			return err
		}
		if len(versions) == 0 {
			continue
		}
		withInstalls = append(withInstalls, populated{tool, versions})
	}

	if len(withInstalls) == 0 {
		fmt.Println()
		fmt.Println(styles.Neutral.Render("Nothing installed yet."))
		fmt.Println(styles.Detail.Render("  Run `sk install <tool>` to get started, or `sk tools` to see what's supported."))
		return nil
	}

	var grandTotal int64
	for _, p := range withInstalls {
		fmt.Println()
		fmt.Println(styles.Header.Render(p.tool.DisplayName + ":"))
		grandTotal += printToolListBody(p.tool, p.versions, showSizes)
	}
	fmt.Println()
	if showSizes {
		fmt.Println(styles.Header.Render(fmt.Sprintf("Grand total: %s", formatMB(grandTotal))))
	}
	fmt.Println(styles.Detail.Render("Tip: `sk list <tool>` shows just one."))
	return nil
}

// printManagedGroup prints the "Managed by SDK Keeper" section,
// grouped by vendor when the tool genuinely has multiple known
// vendors -- matching the same "grouping requires genuine ambiguity"
// principle already used for the vendor picker and version-suffix
// logic in install.go. A flat list otherwise (a single-vendor tool,
// or one with no registered providers at all), since there's nothing
// meaningful to group by.
//
// Vendor grouping is trustworthy HERE specifically because these
// suffixes are data sk itself generated during a real, verified
// install (see newListCmd's own comment for why the not-managed
// section deliberately never attempts this same grouping). An older,
// bare-named managed entry from before vendor suffixes existed at all
// (no recognizable suffix) falls into its own "Other" bucket, rather
// than being silently mis-grouped or dropped.
func printManagedGroup(tool tooldef.Tool, header string, group []inventory.Version, annotate func(inventory.Version) (string, string, string)) bool {
	if len(group) == 0 {
		return false
	}

	vendorNames := vendorNamesFor(tool.Name)
	if len(vendorNames) < 2 {
		return printGroup(header, group, annotate, "  ")
	}

	byVendor := make(map[string][]inventory.Version, len(vendorNames))
	var unrecognized []inventory.Version
	for _, v := range group {
		vendor, ok := versionVendor(v.Number, vendorNames)
		if !ok {
			unrecognized = append(unrecognized, v)
			continue
		}
		byVendor[vendor] = append(byVendor[vendor], v)
	}

	// One width shared across EVERY vendor's own table, computed from
	// ALL of them together -- not each vendor sizing itself to only
	// its own rows. A real, live-reported bug this fixes: when one
	// vendor's longest version number was a couple of characters
	// shorter than another's, that vendor's whole table started its
	// path column at a visibly different position, reading as
	// inconsistent, "wrongly indented" alignment between two blocks
	// that are otherwise meant to look like one continuous table.
	versionColWidth := versionColumnWidth(group)

	fmt.Println(header)
	for _, vendorName := range vendorNames {
		sub := byVendor[vendorName]
		if len(sub) == 0 {
			continue
		}
		fmt.Printf("  %s:\n", capitalize(vendorName))
		printVersions(sub, annotate, "    ", versionColWidth)
	}
	if len(unrecognized) > 0 {
		fmt.Println("  Other:")
		printVersions(unrecognized, annotate, "    ", versionColWidth)
	}
	return true
}

// versionVendor extracts the vendor a version label was suffixed
// with, by matching against the tool's own known vendor names, e.g.
// "21.0.9-temurin" -> "temurin".
func versionVendor(number string, knownVendors []string) (string, bool) {
	for _, name := range knownVendors {
		if strings.HasSuffix(number, "-"+name) {
			return name, true
		}
	}
	return "", false
}

// printGroup prints a header followed by each version in the group.
// Returns false (and prints nothing) if the group is empty.
func printGroup(header string, group []inventory.Version, annotate func(inventory.Version) (string, string, string), indent string) bool {
	if len(group) == 0 {
		return false
	}
	fmt.Println(header)
	printVersions(group, annotate, indent, versionColumnWidth(group))
	return true
}

// versionColumnWidth returns the widest version.Number across group --
// the fixed width every version gets padded to, so the path column
// lines up at the same position across multiple SEPARATE printVersions
// calls (one per vendor) -- see printVersions' own doc comment for why
// plain padding is used here rather than lipgloss/table.
func versionColumnWidth(group []inventory.Version) int {
	width := 0
	for _, v := range group {
		if len(v.Number) > width {
			width = len(v.Number)
		}
	}
	return width
}

// width pad -- NOT lipgloss/table, deliberately. A real, live-
// reported bug found while building an earlier, table-based version
// printVersions prints each version, manually aligned via a fixed-
// width pad -- NOT lipgloss/table, deliberately. A real, live-
// reported bug found while building an earlier, table-based version
// of this function: lipgloss/table redistributes column boundaries
// based on each individual call's OWN row content, even when given
// the exact same target total width -- confirmed directly, by
// measuring the exact byte offset the path column started at for
// Temurin vs Liberica's separately-rendered tables, and finding them
// still a column apart after sharing a total width, and AGAIN after
// additionally padding the version text to a shared width. table.New()
// is built for ONE self-contained table, not several separately
// rendered but visually aligned ones -- which is exactly what's
// needed here, since each vendor prints as its own, separate call.
// Plain fixed-width padding has no such ambiguity: the identical
// computed width byte-for-byte produces the identical column
// boundary, regardless of what's rendered before or after it -- which
// is how this worked correctly before lipgloss/table was introduced
// at all, and is the right tool for this specific shape of problem.
// search.go's own tables are unaffected by any of this -- each of
// those is always a single, self-contained render, never split across
// several separate calls that need to visually line up with each
// other.
func printVersions(group []inventory.Version, annotate func(inventory.Version) (string, string, string), indent string, versionWidth int) {
	for _, v := range group {
		current, def, size := annotate(v)
		// "(current)" and "(default)" each keep their own distinct
		// color (styles.Active vs styles.Default -- see
		// Styles.Default's own doc comment for why: a real,
		// previously-fixed gap where both tags looked identical,
		// unstyled, hard to tell apart at a glance, the whole point
		// of having two separate tags in the first place) -- styled
		// directly here, not via a generic "join tags" helper, so
		// each one keeps its own color even when both appear on the
		// same row.
		var tags []string
		if current != "" {
			tags = append(tags, styles.Active.Render(current))
		}
		if def != "" {
			tags = append(tags, styles.Default.Render(def))
		}
		tag := ""
		if len(tags) > 0 {
			tag = " " + strings.Join(tags, " ")
		}
		sizeCol := ""
		if size != "" {
			sizeCol = "  " + styles.Detail.Render(size)
		}
		fmt.Printf("%s%-*s  %s%s%s\n", indent, versionWidth, v.Number, displayPath(v), tag, sizeCol)
	}
}

// buildPickerGroups turns a flat inventory.Scan result into
// picker.Group values, grouped by vendor -- for `sk use`/`sk remove`
// with no version given, where the picker previously showed every
// installed version as one undifferentiated flat list, interleaving
// different vendors' versions with no indication which was which.
//
// Mirrors printManagedGroup's own real grouping rule exactly (same
// vendorNamesFor/versionVendor helpers, same "only group when the
// tool genuinely has 2+ vendors" condition, same "Other" bucket for
// an unrecognized suffix, same separate "Not managed" section for
// add-registered entries) -- so the picker groups versions exactly
// the same way `sk list` already displays them as plain text, rather
// than inventing a second, subtly different grouping convention.
//
// Each entry is shown as just its Number, matching exactly what the
// picker already displayed before this grouping existed -- this
// change is scoped to adding headers, not to also showing paths or
// any other new information per entry.
func buildPickerGroups(tool tooldef.Tool, versions []inventory.Version) []picker.Group {
	var managed, external []inventory.Version
	for _, v := range versions {
		if v.External {
			external = append(external, v)
		} else {
			managed = append(managed, v)
		}
	}

	numbersOf := func(vs []inventory.Version) []string {
		out := make([]string, len(vs))
		for i, v := range vs {
			out[i] = v.Number
		}
		return out
	}

	var groups []picker.Group

	vendorNames := vendorNamesFor(tool.Name)
	if len(vendorNames) < 2 {
		if len(managed) > 0 {
			groups = append(groups, picker.Group{Items: numbersOf(managed)})
		}
	} else {
		byVendor := make(map[string][]inventory.Version, len(vendorNames))
		var unrecognized []inventory.Version
		for _, v := range managed {
			vendor, ok := versionVendor(v.Number, vendorNames)
			if !ok {
				unrecognized = append(unrecognized, v)
				continue
			}
			byVendor[vendor] = append(byVendor[vendor], v)
		}
		for _, vendorName := range vendorNames {
			sub := byVendor[vendorName]
			if len(sub) == 0 {
				continue
			}
			groups = append(groups, picker.Group{Header: capitalize(vendorName), Items: numbersOf(sub)})
		}
		if len(unrecognized) > 0 {
			groups = append(groups, picker.Group{Header: "Other", Items: numbersOf(unrecognized)})
		}
	}

	if len(external) > 0 {
		// A header even when it's the ONLY group (unlike the
		// single-vendor "no header at all" case above) -- "Not
		// managed" is genuinely informative here, distinguishing
		// externally add-registered entries from sk-installed ones,
		// which a bare, unlabeled list would lose entirely.
		groups = append(groups, picker.Group{Header: "Not managed", Items: numbersOf(external)})
	}

	return groups
}

// listInstalledEntry/listData are list's --format=json success shape.
// SizeBytes is a pointer so it's omitted entirely (not present as
// null or 0) unless --sizes was actually passed -- a plain
// `sk list --format=json` shouldn't pay the directory-walk cost, or
// have callers wonder whether a 0 means "empty install" or "wasn't
// computed".
type listInstalledEntry struct {
	Version   string  `json:"version"`
	Vendor    *string `json:"vendor"`
	IsDefault bool    `json:"isDefault"`
	IsCurrent bool    `json:"isCurrent"`
	SizeBytes *int64  `json:"sizeBytes,omitempty"`
}

type listData struct {
	Tool      string               `json:"tool"`
	Installed []listInstalledEntry `json:"installed"`
}

// buildListJSON reports a single flat "installed" array -- the
// managed/external distinction and vendor grouping are text-rendering
// concerns this schema doesn't cover, though external entries are
// still included in the flat list. Uses the same findActiveVersion/
// readDefault calls as buildCurrentJSON, against the same
// inventory.Scan result, so list's isCurrent can never disagree with
// current's own active value.
func buildListJSON(toolName string, showSizes bool) (*listData, *jsonError) {
	tool, ok := tooldef.Get(toolName)
	if !ok {
		return nil, &jsonError{Code: ErrCodeAmbiguousTool, Message: fmt.Sprintf("unknown tool: %s", toolName)}
	}

	versions, err := inventory.Scan(tool)
	if err != nil {
		return nil, &jsonError{Code: ErrCodeInternalError, Message: err.Error()}
	}

	var currentVersion string
	if tool.EnvVar != "" {
		if envVal, isSet := os.LookupEnv(tool.EnvVar); isSet {
			if v, ok := findActiveVersion(tool, versions, envVal); ok {
				currentVersion = v.Number
			}
		}
	}
	defaultVersion, _ := readDefault(tool)

	entries := make([]listInstalledEntry, 0, len(versions))
	for _, v := range versions {
		entry := listInstalledEntry{
			Version:   v.Number,
			Vendor:    vendorOf(tool.Name, v.Number),
			IsDefault: v.Number == defaultVersion,
			IsCurrent: v.Number == currentVersion,
		}
		if showSizes {
			n := dirSize(v.Path)
			entry.SizeBytes = &n
		}
		entries = append(entries, entry)
	}

	return &listData{Tool: tool.Name, Installed: entries}, nil
}

// allToolsListData is `sk list` with no arguments' --format=json
// shape: one listData per tool that actually has something
// installed, reusing buildListJSON itself so the all-tools payload
// can never disagree with what `sk list <tool> --format=json` reports
// for that same tool individually.
//
// Matches printAllToolsList's own text output exactly -- empty tools
// are excluded from BOTH, not just the human-facing one. An earlier
// version of this kept JSON complete (every registered tool, empty
// ones included) on the theory that a script benefits from full
// enumeration in one call; that didn't hold up: `list`'s job is
// showing what's INSTALLED, that's true regardless of output format,
// and sk already has a dedicated, correct place for "what does sk
// support, installed or not" -- `sk tools --format=json`. Keeping
// this one complete too would just be the same data reachable two
// different ways, at the cost of text and JSON silently answering two
// different questions under one command name.
type allToolsListData struct {
	Tools []listData `json:"tools"`
}

// buildAllToolsListJSON mirrors printAllToolsList's own filtering
// exactly (same sortedTools() order, same "skip anything with zero
// installed" rule) -- see allToolsListData's doc comment for why.
func buildAllToolsListJSON(showSizes bool) *allToolsListData {
	tools := make([]listData, 0, len(tooldef.Registry))
	for _, tool := range sortedTools() {
		data, jerr := buildListJSON(tool.Name, showSizes)
		if jerr != nil {
			// Genuinely unreachable given the loop is driven by
			// tooldef.Registry itself, but handled explicitly rather
			// than silently dropping a tool from the payload.
			data = &listData{Tool: tool.Name, Installed: []listInstalledEntry{}}
		}
		if len(data.Installed) == 0 {
			continue
		}
		tools = append(tools, *data)
	}
	return &allToolsListData{Tools: tools}
}

package cli

import (
	"fmt"
	"os"
	"sort"
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

			var sizes map[string]int64
			var total int64
			if showSizes {
				sizes, total = computeSizes(versions)
			}

			fmt.Println()
			// grandTotal=0 here means "omit the percentage line" --
			// there's no grand total to compare against for a single
			// tool on its own, unlike the all-tools case below.
			printToolListBody(tool, versions, sizes, total, 0, showSizes)
			return nil
		},
	}
	cmd.Flags().Bool("sizes", false, "show real on-disk size per installed version, plus subtotals")
	return cmd
}

// computeSizes walks every version's real directory (see dirSize) and
// returns both a per-version lookup and the combined total. Kept
// separate from printing so printAllToolsList can compute every
// tool's numbers in a first pass, before printing anything -- the
// "X% of grand total" line needs the GRAND total, which isn't known
// until every tool has been walked.
func computeSizes(versions []inventory.Version) (map[string]int64, int64) {
	sizes := make(map[string]int64, len(versions))
	var total int64
	for _, v := range versions {
		n := dirSize(v.Path)
		sizes[v.Number] = n
		total += n
	}
	return sizes, total
}

// sizeBar draws a compact, fixed-width relative-size indicator, e.g.
// "━━━━━━━━──" -- filled proportionally to size/maxSize, so an entry
// that dwarfs the others in the same listing is visually obvious at a
// glance, not just from reading the numbers. maxSize is the largest
// SINGLE entry across the tool's WHOLE listing (managed and external
// together, computed once in printToolListBody) -- not scoped to just
// one vendor's own sub-list -- so a manually add-registered JDK that
// dwarfs the managed ones reads as visually obvious too, not just
// relative to its own small group. Same thin-line technique as the
// download progress bar (renderDownloadProgress), just much narrower:
// this is an inline per-row indicator, not a full standalone progress
// display. Uses ━/─ (heavy/light horizontal line) rather than the
// solid █/░ blocks an earlier version used -- the full-height blocks
// read as visually "too thick" for a compact inline indicator sitting
// next to plain text on the same line; a thin line matches the rest
// of the row's own weight better.
func sizeBar(size, maxSize int64) string {
	const barWidth = 10
	if maxSize <= 0 {
		return strings.Repeat("\u2500", barWidth)
	}
	filled := int(float64(barWidth) * float64(size) / float64(maxSize))
	if filled > barWidth {
		filled = barWidth
	}
	if filled < 0 {
		filled = 0
	}
	//return strings.Repeat("\u2588", filled) + strings.Repeat("\u2591", barWidth-filled)
	return strings.Repeat("\u2501", filled) + strings.Repeat("\u2500", barWidth-filled)
}

// printToolListBody renders one tool's installed versions -- exactly
// today's single-tool `sk list <tool>` body, extracted so
// printAllToolsList can call it once per tool without duplicating any
// of this logic. Deliberately prints no leading blank line of its
// own -- the caller controls spacing, since that differs between the
// single-tool case (one blank line before this) and the all-tools
// case (a tool-name header immediately before this, no extra blank
// between them).
//
// sizes/toolTotal are precomputed by the caller (via computeSizes),
// not walked again here -- printAllToolsList needs the SAME numbers
// for its own grand-total pre-pass, and walking every directory
// twice would be wasteful. grandTotal is 0 for the single-tool case
// (nothing to show a percentage of); printAllToolsList passes the
// real grand total so each tool's subtotal can show its own share.
func printToolListBody(tool tooldef.Tool, versions []inventory.Version, sizes map[string]int64, toolTotal int64, grandTotal int64, showSizes bool) {
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

	// The scale every per-entry bar is drawn relative to -- the
	// largest SINGLE entry across this tool's whole listing, managed
	// and external combined. See sizeBar's own doc comment for why
	// that scope, not a narrower per-vendor one.
	var maxSize int64
	if showSizes {
		for _, n := range sizes {
			if n > maxSize {
				maxSize = n
			}
		}
	}

	// Returns the PLAIN (unstyled) tag text for a version -- coloring
	// happens later, applied separately to each piece at render time,
	// not baked into these strings. A real, confirmed reason for that
	// split: lipgloss/table miscalculates column widths when cell
	// content carries embedded ANSI codes (verified directly -- it
	// silently truncated real version numbers when a neighboring cell
	// was pre-styled), so styling must stay separate from the text
	// itself wherever alignment depends on the plain-text length.
	annotate := func(v inventory.Version) (currentTag, defaultTag, sizeTag, barTag string) {
		if v.Number == currentVersion {
			currentTag = "(current)"
		}
		if v.Number == defaultVersion {
			defaultTag = "(default)"
		}
		if showSizes {
			n := sizes[v.Number]
			sizeTag = formatMB(n)
			barTag = sizeBar(n, maxSize)
		}
		return currentTag, defaultTag, sizeTag, barTag
	}

	// Grouped by managed/not-managed with a header stated ONCE per
	// group, rather than repeating the full phrase on every single
	// line -- an earlier version of this did the latter and became a
	// genuinely crowded wall of text once more than a couple of
	// entries were present. Each group's versions stay in their
	// existing sort order (newest-first from inventory.Scan) UNLESS
	// showSizes is on, in which case printManagedGroup/printGroup
	// re-sort by size descending instead -- see their own comments
	// for why. An empty group's header is simply omitted rather than
	// printed with nothing underneath it.
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
		return
	}

	printedManaged := printManagedGroup(tool, styles.Header.Render("Managed by SDK Keeper:"), managed, annotate, sizes)
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
	// verified install. Rendered in the distinct External style (see
	// its own doc comment in term/style.go) -- not managed reads as
	// visually different at a glance, not just via the header text.
	printGroup(styles.External.Render("Not managed by SDK Keeper:"), external, annotate, sizes, "  ", true)
	if showSizes && len(external) > 0 {
		fmt.Println(styles.Detail.Render(
			"  Tip: sizes above are real, but not managed by SDK Keeper -- `sk remove` won't free that space; delete manually if no longer needed.",
		))
	}

	if showSizes {
		fmt.Println()
		line := fmt.Sprintf("  %s total: %s", tool.DisplayName, formatMB(toolTotal))
		if grandTotal > 0 {
			pct := float64(toolTotal) / float64(grandTotal) * 100
			line += fmt.Sprintf(" (%.0f%% of grand total)", pct)
		}
		fmt.Println(styles.Detail.Render(line))
	}
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
// found" lines drowning the 2-3 tools someone actually has), served
// no one. --format=json filters the same way, for the same reason --
// see buildAllToolsListJSON's own doc comment.
//
// Runs in two passes when showSizes is on: every tool's sizes/total
// are computed FIRST (nothing printed yet), so the real grand total
// is known before any per-tool subtotal is printed -- each one can
// then show its own share of the grand total, which would otherwise
// be unknowable until after every tool had already been walked and
// printed.
func printAllToolsList(showSizes bool) error {
	type populated struct {
		tool     tooldef.Tool
		versions []inventory.Version
		sizes    map[string]int64
		total    int64
	}
	var withInstalls []populated
	var grandTotal int64
	for _, tool := range sortedTools() {
		versions, err := inventory.Scan(tool)
		if err != nil {
			return err
		}
		if len(versions) == 0 {
			continue
		}
		p := populated{tool: tool, versions: versions}
		if showSizes {
			p.sizes, p.total = computeSizes(versions)
			grandTotal += p.total
		}
		withInstalls = append(withInstalls, p)
	}

	if len(withInstalls) == 0 {
		fmt.Println()
		fmt.Println(styles.Neutral.Render("Nothing installed yet."))
		fmt.Println(styles.Detail.Render("  Run `sk install <tool>` to get started, or `sk tools` to see what's supported."))
		return nil
	}

	for _, p := range withInstalls {
		fmt.Println()
		fmt.Println(styles.Header.Render(p.tool.DisplayName + ":"))
		printToolListBody(p.tool, p.versions, p.sizes, p.total, grandTotal, showSizes)
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
//
// When sizes is non-nil, each vendor's own sub-list (and the "Other"
// bucket) is re-sorted by size descending before printing -- the
// existing newest-first order stops being the useful one the moment
// someone reaches for --sizes at all: they're almost certainly asking
// "what's eating my disk", and the biggest offender belongs at the
// top, not wherever it happens to fall in version order.
func printManagedGroup(tool tooldef.Tool, header string, group []inventory.Version, annotate func(inventory.Version) (string, string, string, string), sizes map[string]int64) bool {
	if len(group) == 0 {
		return false
	}

	vendorNames := vendorNamesFor(tool.Name)
	if len(vendorNames) < 2 {
		return printGroup(header, group, annotate, sizes, "  ", false)
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
	// size/path columns at a visibly different position, reading as
	// inconsistent, "wrongly indented" alignment between two blocks
	// that are otherwise meant to look like one continuous table.
	versionColWidth := versionColumnWidth(group)
	sizeColWidth := sizeColumnWidth(group, sizes)

	fmt.Println(header)
	for _, vendorName := range vendorNames {
		sub := byVendor[vendorName]
		if len(sub) == 0 {
			continue
		}
		sortBySizeDesc(sub, sizes)
		fmt.Printf("  %s:\n", capitalize(vendorName))
		printVersions(sub, annotate, "    ", versionColWidth, sizeColWidth, false)
	}
	if len(unrecognized) > 0 {
		sortBySizeDesc(unrecognized, sizes)
		fmt.Println("  Other:")
		printVersions(unrecognized, annotate, "    ", versionColWidth, sizeColWidth, false)
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
// Returns false (and prints nothing) if the group is empty. When
// sizes is non-nil, group is re-sorted by size descending first --
// see printManagedGroup's own comment for why. external controls
// whether printVersions renders each row in the distinct External
// style (see term/style.go).
func printGroup(header string, group []inventory.Version, annotate func(inventory.Version) (string, string, string, string), sizes map[string]int64, indent string, external bool) bool {
	if len(group) == 0 {
		return false
	}
	sortBySizeDesc(group, sizes)
	fmt.Println(header)
	printVersions(group, annotate, indent, versionColumnWidth(group), sizeColumnWidth(group, sizes), external)
	return true
}

// sortBySizeDesc reorders versions by their real size, largest first,
// in place. A no-op when sizes is nil (showSizes wasn't requested) --
// the default newest-first order from inventory.Scan is left exactly
// as it was.
func sortBySizeDesc(versions []inventory.Version, sizes map[string]int64) {
	if sizes == nil {
		return
	}
	sort.SliceStable(versions, func(i, j int) bool {
		return sizes[versions[i].Number] > sizes[versions[j].Number]
	})
}

// versionColumnWidth returns the widest version.Number across group --
// the fixed width every version gets padded to, so the size column
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

// sizeColumnWidth mirrors versionColumnWidth, for the formatted size
// text (e.g. "205.3 MB") instead of the version number -- the fixed
// width the RIGHT-ALIGNED size column pads to, computed once across
// the whole group being printed together, for the same alignment
// reason versionColumnWidth exists. Returns 0 when sizes is nil
// (nothing to align).
func sizeColumnWidth(group []inventory.Version, sizes map[string]int64) int {
	if sizes == nil {
		return 0
	}
	width := 0
	for _, v := range group {
		if n := len(formatMB(sizes[v.Number])); n > width {
			width = n
		}
	}
	return width
}

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
//
// Column order is version, then (bar +) size right-aligned, then any
// tags, then path last -- version and size are the two columns that
// must actually line up row to row, so nothing of variable width sits
// between them; tags are short, variable-presence annotations placed
// after size instead, and path -- the least urgent thing to compare
// at a glance -- trails at the end, styled External/muted+italic for
// a not-managed row so the whole row reads as visually distinct, not
// just its header above it.
func printVersions(group []inventory.Version, annotate func(inventory.Version) (string, string, string, string), indent string, versionWidth, sizeWidth int, external bool) {
	for _, v := range group {
		current, def, size, bar := annotate(v)
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
		tagStr := ""
		if len(tags) > 0 {
			tagStr = " " + strings.Join(tags, " ")
		}

		sizeCol := ""
		if size != "" {
			sizeCol = fmt.Sprintf("  %s %*s", bar, sizeWidth, size)
		}

		path := displayPath(v)
		if external {
			path = styles.External.Render(path)
		}

		fmt.Printf("%s%-*s%s%s  %s\n", indent, versionWidth, v.Number, sizeCol, tagStr, path)
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
// Managed distinguishes a real sk-managed install (`sk remove` frees
// its disk space) from an `add`-registered external entry (`sk
// remove` only deletes the symlink; the real files, and SizeBytes'
// real number, live on regardless) -- the machine-readable
// counterpart to the plain-text "Tip: ... not managed by SDK Keeper"
// line, so a script summing SizeBytes can make the same distinction a
// human reading the text output would.
//
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
	Managed   bool    `json:"managed"`
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
			Managed:   !v.External,
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

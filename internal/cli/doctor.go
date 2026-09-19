package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"sdkkeeper/internal/inventory"
	"sdkkeeper/internal/tooldef"
)

// doctorIssue is one problem found by a single check, printed as an
// indented sub-line under that check's summary line.
type doctorIssue struct {
	warning bool // cosmetic/safe-to-ignore vs. an actual failure
	message string
	fix     string // optional
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check SDK Keeper's own managed state for problems",
		Args:  requireArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			if outputFormat == FormatJSON {
				data := buildDoctorJSON(cmd.Context())
				return emitDoctorJSON(data)
			}

			fmt.Fprintln(session.Out)
			fmt.Fprintln(session.Out, styles.Header.Render("Checking SDK Keeper..."))
			fmt.Fprintln(session.Out)

			total := 0
			total += printCheck("dangling registration", "dangling registrations", checkDanglingRegistrations())
			total += printCheck("incomplete install", "incomplete installs", checkIncompleteInstalls())
			total += printCheck("stale default", "stale defaults", checkStaleDefaults())
			total += printCheck("leftover temp directory", "leftover temp directories", checkLeftoverTempDirs())
			total += printVendorReachability(cmd.Context())

			fmt.Fprintln(session.Out)
			if total == 0 {
				fmt.Fprintln(session.Out, styles.Success.Render("\u2713 Everything looks healthy."))
				return nil
			}
			plural := "s"
			if total == 1 {
				plural = ""
			}
			fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf("%d problem%s found.", total, plural)))
			return fmt.Errorf("%d problems found", total)
		},
	}
}

// printCheck renders one check's result: "✓ No X" if issues is empty,
// or a "✗/⚠ N X found:" header with each issue as a sub-line. Returns
// the count printed, for the overall summary tally.
func printCheck(singular, plural string, issues []doctorIssue) int {
	if len(issues) == 0 {
		fmt.Fprintln(session.Out, styles.Success.Render(fmt.Sprintf("\u2713 No %s", plural)))
		return 0
	}

	label := plural
	if len(issues) == 1 {
		label = singular
	}
	glyph := "\u2717"
	style := styles.Error
	if allWarnings(issues) {
		glyph = "\u26a0"
		style = styles.Warning
	}
	fmt.Fprintln(session.Out, style.Render(fmt.Sprintf("%s %d %s found:", glyph, len(issues), label)))
	for _, issue := range issues {
		fmt.Fprintln(session.Out, style.Render("    "+issue.message))
		if issue.fix != "" {
			fmt.Fprintln(session.Out, styles.Detail.Render("    \u2192 "+issue.fix))
		}
	}
	return len(issues)
}

func allWarnings(issues []doctorIssue) bool {
	for _, issue := range issues {
		if !issue.warning {
			return false
		}
	}
	return true
}

// checkDanglingRegistrations finds every `add`-registered (symlinked)
// entry whose real target no longer resolves.
func checkDanglingRegistrations() []doctorIssue {
	var issues []doctorIssue
	for _, tool := range sortedTools() {
		versions, err := inventory.Scan(tool)
		if err != nil {
			continue
		}
		for _, v := range versions {
			if !v.External {
				continue
			}
			if _, err := os.Stat(v.Path); err != nil {
				issues = append(issues, doctorIssue{
					message: fmt.Sprintf("%s %s is registered, but its real target no longer exists", tool.DisplayName, v.Number),
					fix:     fmt.Sprintf("run `sk remove %s %s` to clear the stale registration", tool.Name, v.Number),
				})
			}
		}
	}
	return issues
}

// checkIncompleteInstalls finds every real, sk-installed version
// directory whose bin directory is missing or empty -- possible after
// a SIGKILL mid-install (which skips Go's defer-based cleanup) or a
// manual partial deletion.
func checkIncompleteInstalls() []doctorIssue {
	var issues []doctorIssue
	for _, tool := range sortedTools() {
		versions, err := inventory.Scan(tool)
		if err != nil {
			continue
		}
		for _, v := range versions {
			if v.External {
				continue
			}
			entries, err := os.ReadDir(tool.BinPath(v.Path))
			if err != nil || len(entries) == 0 {
				issues = append(issues, doctorIssue{
					message: fmt.Sprintf("%s %s looks incomplete (missing or empty bin directory)", tool.DisplayName, v.Number),
					fix:     fmt.Sprintf("run `sk remove %s %s` and reinstall it", tool.Name, v.Number),
				})
			}
		}
	}
	return issues
}

// checkStaleDefaults finds every tool whose stored default no longer
// corresponds to anything installed -- possible after a manual
// `rm -rf` or a hand-edited defaults file.
func checkStaleDefaults() []doctorIssue {
	var issues []doctorIssue
	for _, tool := range sortedTools() {
		stored, err := readDefault(tool)
		if err != nil || stored == "" {
			continue
		}
		versions, err := inventory.Scan(tool)
		if err != nil {
			continue
		}
		found := false
		for _, v := range versions {
			if v.Number == stored {
				found = true
				break
			}
		}
		if !found {
			issues = append(issues, doctorIssue{
				message: fmt.Sprintf("%s's default (%s) no longer exists", tool.DisplayName, stored),
				fix:     fmt.Sprintf("run `sk default %s <version>` to set a new one, or `sk default %s null` to clear it", tool.Name, tool.Name),
			})
		}
	}
	return issues
}

// checkLeftoverTempDirs finds anything left in ~/.sdkkeeper/tmp,
// which should be emptied after every install but can survive a
// SIGKILL mid-download. Marked as warnings, not errors -- harmless
// clutter.
func checkLeftoverTempDirs() []doctorIssue {
	entries, err := os.ReadDir(tooldef.TempRoot())
	if err != nil {
		return nil // TempRoot not existing at all is the common, healthy case
	}

	var issues []doctorIssue
	for _, e := range entries {
		issues = append(issues, doctorIssue{
			warning: true,
			message: fmt.Sprintf("%s", filepath.Join(tooldef.TempRoot(), e.Name())),
			fix:     fmt.Sprintf("safe to delete: rm -rf %s", filepath.Join(tooldef.TempRoot(), e.Name())),
		})
	}
	return issues
}

// printVendorReachability checks each vendor's API, only here inside
// doctor as an explicit, user-initiated check (unlike SDKMAN, which
// does this on every startup). A short timeout avoids hanging on an
// offline machine. Iterates every registered tool/provider
// generically, so a future tool is picked up automatically.
func printVendorReachability(ctx context.Context) int {
	problems := 0
	for _, tool := range sortedTools() {
		for _, name := range vendorNamesFor(tool.Name) {
			provider := providersFor(tool.Name)[name]
			checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, err := provider.ListMajorVersions(checkCtx)
			cancel()
			if err != nil {
				fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf("\u2717 %s API unreachable: %s", capitalize(name), err)))
				problems++
				continue
			}
			fmt.Fprintln(session.Out, styles.Success.Render(fmt.Sprintf("\u2713 %s API reachable", capitalize(name))))
		}
	}
	return problems
}

// toolOrder is the deliberate presentation order -- Java first, then
// Maven and Gradle, which both require Java (tooldef.Tool.RequiresJava)
// -- not alphabetical (plain sort.Strings would put Gradle first).
var toolOrder = []string{"java", "maven", "gradle"}

// sortedTools returns every registered tool in toolOrder, appending
// any tool not listed there at the end, alphabetically.
func sortedTools() []tooldef.Tool {
	seen := make(map[string]bool, len(toolOrder))
	tools := make([]tooldef.Tool, 0, len(tooldef.Registry))

	for _, name := range toolOrder {
		if tool, ok := tooldef.Registry[name]; ok {
			tools = append(tools, tool)
			seen[name] = true
		}
	}

	var remaining []string
	for name := range tooldef.Registry {
		if !seen[name] {
			remaining = append(remaining, name)
		}
	}
	sort.Strings(remaining)
	for _, name := range remaining {
		tools = append(tools, tooldef.Registry[name])
	}

	return tools
}

// doctorCheckJSON/doctorSummaryJSON/doctorData are doctor's
// --format=json shape.
type doctorCheckJSON struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type doctorSummaryJSON struct {
	Pass int `json:"pass"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
}

type doctorData struct {
	Checks  []doctorCheckJSON `json:"checks"`
	Summary doctorSummaryJSON `json:"summary"`
}

// buildDoctorJSON reuses the same, pure checkX functions the
// interactive path's printCheck calls, so JSON and text can never
// disagree about which issues were found. Vendor reachability is
// folded into one named check here, rather than per-vendor.
func buildDoctorJSON(ctx context.Context) *doctorData {
	type namedCheck struct {
		name   string
		plural string
		issues []doctorIssue
	}
	groups := []namedCheck{
		{"dangling_registrations", "dangling registrations", checkDanglingRegistrations()},
		{"incomplete_installs", "incomplete installs", checkIncompleteInstalls()},
		{"stale_defaults", "stale defaults", checkStaleDefaults()},
		{"leftover_temp_dirs", "leftover temp directories", checkLeftoverTempDirs()},
	}

	var checks []doctorCheckJSON
	var summary doctorSummaryJSON
	for _, g := range groups {
		c := doctorCheckJSONFrom(g.name, g.plural, g.issues)
		checks = append(checks, c)
		tallyDoctorStatus(&summary, c.Status)
	}

	vendorCheck := buildVendorReachabilityCheckJSON(ctx)
	checks = append(checks, vendorCheck)
	tallyDoctorStatus(&summary, vendorCheck.Status)

	return &doctorData{Checks: checks, Summary: summary}
}

// doctorCheckJSONFrom mirrors printCheck's own pass/warn/fail rule,
// building a struct instead of printing.
func doctorCheckJSONFrom(name, plural string, issues []doctorIssue) doctorCheckJSON {
	if len(issues) == 0 {
		return doctorCheckJSON{Name: name, Status: "pass", Message: fmt.Sprintf("No %s", plural)}
	}
	status := "fail"
	if allWarnings(issues) {
		status = "warn"
	}
	messages := make([]string, len(issues))
	for i, issue := range issues {
		messages[i] = issue.message
	}
	return doctorCheckJSON{Name: name, Status: status, Message: strings.Join(messages, "; ")}
}

// buildVendorReachabilityCheckJSON mirrors printVendorReachability's
// loop, collecting results into one named check.
func buildVendorReachabilityCheckJSON(ctx context.Context) doctorCheckJSON {
	var problems []string
	reachable := 0
	for _, tool := range sortedTools() {
		for _, name := range vendorNamesFor(tool.Name) {
			provider := providersFor(tool.Name)[name]
			checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, err := provider.ListMajorVersions(checkCtx)
			cancel()
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s API unreachable: %s", capitalize(name), err))
				continue
			}
			reachable++
		}
	}
	if len(problems) == 0 {
		return doctorCheckJSON{Name: "vendor_reachability", Status: "pass", Message: fmt.Sprintf("%d vendor API(s) reachable", reachable)}
	}
	return doctorCheckJSON{Name: "vendor_reachability", Status: "fail", Message: strings.Join(problems, "; ")}
}

// tallyDoctorStatus adds one check's status into the running summary
// counts.
func tallyDoctorStatus(summary *doctorSummaryJSON, status string) {
	switch status {
	case "pass":
		summary.Pass++
	case "warn":
		summary.Warn++
	case "fail":
		summary.Fail++
	}
}

// emitDoctorJSON is doctor's own emitter, not the shared emitJSON:
// a failing check is a finding, not an invocation error, so the
// envelope always reports success -- but still returns a *CLIError
// for exit code 201 when the summary shows at least one failure.
func emitDoctorJSON(data *doctorData) error {
	writeJSONEnvelope(jsonEnvelope{SchemaVersion: schemaVersion, Status: "ok", Data: data})
	if data.Summary.Fail > 0 {
		return &CLIError{Code: ErrCodeDoctorCheckFailed, Err: fmt.Errorf("%d check(s) failed", data.Summary.Fail)}
	}
	return nil
}

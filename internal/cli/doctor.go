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

// doctorIssue is one problem found by a single check -- printed as an
// indented sub-line under that check's own summary line.
type doctorIssue struct {
	// warning marks a cosmetic, safe-to-ignore issue (e.g. leftover
	// temp files) rather than something that would actually break a
	// real command -- rendered with a different glyph so the two
	// categories aren't visually conflated.
	warning bool
	message string
	fix     string // optional; empty if there's nothing actionable to suggest
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

// printCheck renders one check's result -- a single "✓ No X" line if
// issues is empty, or a "✗/⚠ N X found:" header followed by each
// issue as an indented sub-line with its own suggested fix. Returns
// the count actually printed, for the overall summary tally.
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
	// A real bug found via actual use: a "⚠" (cosmetic, safe-to-
	// ignore) issue was previously rendered in the exact SAME red as
	// a genuine "✗" failure -- making the two visually
	// indistinguishable, which defeats the entire point of having a
	// separate warning glyph at all. Now uses its own, distinct
	// amber Warning style -- see Styles' own doc comment for the
	// full convention this follows.
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
// entry whose real target no longer resolves -- the target was moved
// or deleted outside sk's knowledge. Directly the same class of bug
// as a real, documented SDKMAN issue found during design research
// (candidates/java/current resolving to a target that had moved),
// just checked explicitly rather than discovered via a confusing
// downstream failure in some other tool.
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

// checkIncompleteInstalls finds every real, sk-installed (non-symlink)
// version directory that looks structurally incomplete -- its bin
// directory is missing or empty. sk's own installer uses an atomic
// rename specifically to prevent this on a clean success/failure
// path, but a SIGKILL mid-install (which skips Go's defer-based
// cleanup entirely) or a manual partial deletion could still produce
// one.
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
// corresponds to anything actually installed or registered. `remove`
// already clears a default when IT deletes the exact matching
// version, but a manual `rm -rf` outside sk, or a hand-edited defaults
// file, would leave a stale reference `remove` never got a chance to
// catch.
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

// checkLeftoverTempDirs finds anything left behind in
// ~/.sdkkeeper/tmp -- this directory is supposed to be emptied after
// every install, success or failure, but a SIGKILL mid-download or
// mid-extraction skips that cleanup entirely (Go's defer statements
// don't run on a killed process), leaving orphaned data behind
// indefinitely. Marked as warnings, not errors -- harmless clutter,
// not something that breaks any real command.
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

// printVendorReachability checks each known vendor's API directly --
// deliberately only here, inside doctor, as an explicit, user-
// initiated check. SDKMAN performs an automatic reachability check on
// EVERY startup; sk deliberately does not, consistent with this whole
// project's "nothing automatic, always explicit" principle. A short
// timeout keeps a fully offline machine from hanging here indefinitely.
//
// Iterates every registered tool's own providers generically (via
// sortedTools + vendorNamesFor), rather than a flat, hardcoded vendor
// list -- a future tool's providers are picked up automatically the
// moment they're registered in providers.go, with zero changes needed
// here.
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

// toolOrder is the deliberate order every tool-iterating check in this
// file presents tools in -- Java first, then Maven and Gradle (which
// both require Java as a prerequisite), matching the real dependency
// relationship already established elsewhere in this codebase
// (tooldef.Tool.RequiresJava), not an alphabetical accident.
//
// A real, confirmed regression this fixes: sortedTools previously
// used sort.Strings() directly on tooldef.Registry's own map keys --
// alphabetically "gradle" < "java" < "maven", so doctor's own output
// actually printed Gradle FIRST, ahead of Java, silently reordering a
// genuinely meaningful sequence into an accidental one. This is the
// exact same class of bug providersByTool's own doc comment already
// describes catching once, for VENDOR ordering within a tool -- this
// is the same mistake at the TOOL level, simply never caught there
// until now.
var toolOrder = []string{"java", "maven", "gradle"}

// sortedTools returns every registered tool in the deliberate order
// above, appending any tool NOT listed there (a future addition to
// tooldef.Registry that toolOrder hasn't been updated for yet) at the
// end, alphabetically -- so a forgotten update here degrades to the
// old, at-least-deterministic behavior for anything unrecognized,
// rather than silently dropping it from doctor's output entirely.
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

// doctorCheckJSON and doctorSummaryJSON/doctorData are `doctor`'s
// --format=json `data` shape (design doc §4).
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

// buildDoctorJSON is `doctor`'s --format=json counterpart. Reuses the
// exact same checkX functions the interactive path's printCheck calls
// -- those are already pure (no printing at all; only printCheck
// itself prints) -- so the JSON and text paths can never disagree
// about which issues were actually found, only about how they're
// rendered. Vendor reachability is folded into ONE named check here
// ("vendor_reachability"), unlike printVendorReachability's own
// per-vendor inline printing, to fit the same {name, status, message}
// shape every other check uses -- one issue-message per unreachable
// vendor, joined together, rather than a separate top-level check per
// vendor with no shared name to group them under.
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

// doctorCheckJSONFrom mirrors printCheck's own pass/warn/fail
// coloring rule exactly (same allWarnings condition), just building a
// struct instead of printing styled text.
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
// own reachability loop exactly, collecting results into one named
// check instead of printing one line per vendor.
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

// emitDoctorJSON is doctor's own emitter, deliberately NOT the shared
// emitJSON every other command uses -- design doc §4's own nuance:
// "the envelope's top-level status is error ONLY if the diagnostic
// process itself couldn't run... never for reporting a failed check.
// A fail check is a finding, not an invocation error." So this always
// writes a SUCCESS envelope (nothing in this package's current check
// functions represents "doctor itself couldn't run" as a distinct
// condition -- every checkX function already swallows its own scan
// errors and continues, matching the interactive path's existing
// leniency), but still returns a *CLIError for exit code 201 (design
// doc §5: "doctor exits 201 only when a check is fail") whenever the
// summary shows at least one failing check -- a genuine case of a
// SUCCESSFUL envelope paired with a non-zero process exit code.
func emitDoctorJSON(data *doctorData) error {
	writeJSONEnvelope(jsonEnvelope{SchemaVersion: schemaVersion, Status: "ok", Data: data})
	if data.Summary.Fail > 0 {
		return &CLIError{Code: ErrCodeDoctorCheckFailed, Err: fmt.Errorf("%d check(s) failed", data.Summary.Fail)}
	}
	return nil
}

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
	fix     string // full manual instructions -- shown as-is in plain `sk doctor`, unaffected by fix/autofix below

	// autofix is nil for anything `sk doctor fix` can't safely
	// automate at all (currently just vendor reachability, which
	// isn't even a doctorIssue -- see printVendorReachability). Where
	// present, it performs ONLY the safe, mechanical part of `fix`
	// above -- e.g. deleting a broken directory, never a network call
	// or a judgment call like picking a new default version.
	autofix func() error

	// residualHint is what's still left to do by hand after a
	// SUCCESSFUL autofix -- empty means autofix fully resolves the
	// issue. Deliberately separate from `fix`: `fix` must stay
	// complete and correct for someone who only ever runs plain
	// `sk doctor` and never `fix` (e.g. incomplete installs' `fix`
	// still says "remove and reinstall", covering the step autofix
	// will have already done).
	residualHint string
}

func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check SDK Keeper's own managed state for problems",
		Example: `  sk doctor        # report only
  sk doctor fix    # fix what's safe to auto-fix, report the rest`,
		Args: requireArgs(cobra.NoArgs),
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
	cmd.AddCommand(newDoctorFixCmd())
	return cmd
}

// newDoctorFixCmd is `sk doctor fix` -- a real subcommand, not a
// --fix flag, matching sk's own established vocabulary: a positional
// word names a distinct action (see `sk init skrc`/`sk remove skrc`'s
// identical reasoning), a flag names a MODE of the same one (like
// --format). Fixing is a different action from reporting, not a mode
// of it.
//
// Only ever performs the safe, mechanical part of what `sk doctor`
// already reports -- see each doctorIssue's own autofix field for
// exactly what that is per check. Never attempts a network call or a
// judgment call (picking a version, choosing a new default) on the
// user's behalf; those stay manual, reported as a residual hint.
func newDoctorFixCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "fix",
		Short:   "Auto-fix what sk doctor can safely fix on its own; report the rest",
		Example: `  sk doctor fix`,
		Args:    requireArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			if outputFormat == FormatJSON {
				data := buildDoctorFixJSON()
				return emitDoctorFixJSON(data)
			}
			return runDoctorFix()
		},
	}
}

// runDoctorFix re-runs the same four structured checks doctor's own
// report uses (so it can never find something different from what
// `sk doctor` just told the user about), and for every issue with a
// non-nil autofix, runs it -- reporting fixed / still-needs-attention
// / failed-to-fix per issue. Vendor reachability isn't a doctorIssue
// at all (see printVendorReachability) and has nothing fixable about
// it, so it's not part of this command.
func runDoctorFix() error {
	fmt.Fprintln(session.Out)
	fmt.Fprintln(session.Out, styles.Header.Render("Fixing what SDK Keeper can fix on its own..."))
	fmt.Fprintln(session.Out)

	groups := []struct {
		singular, plural string
		issues           []doctorIssue
	}{
		{"dangling registration", "dangling registrations", checkDanglingRegistrations()},
		{"incomplete install", "incomplete installs", checkIncompleteInstalls()},
		{"stale default", "stale defaults", checkStaleDefaults()},
		{"leftover temp directory", "leftover temp directories", checkLeftoverTempDirs()},
	}

	fixed, needsAttention, failed := 0, 0, 0
	anyIssue := false
	for _, g := range groups {
		if len(g.issues) == 0 {
			fmt.Fprintln(session.Out, styles.Success.Render(fmt.Sprintf("\u2713 No %s", g.plural)))
			continue
		}
		anyIssue = true
		for _, issue := range g.issues {
			if issue.autofix == nil {
				// Nothing here is currently unfixable-but-reported at
				// the per-issue level (every check that produces
				// doctorIssue values has an autofix today) -- kept as
				// a real branch anyway, since a future check might add
				// an issue with no safe fix at all.
				fmt.Fprintln(session.Out, styles.Warning.Render("\u26a0 "+issue.message))
				fmt.Fprintln(session.Out, styles.Detail.Render("    \u2192 "+issue.fix))
				needsAttention++
				continue
			}
			if err := issue.autofix(); err != nil {
				fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf("\u2717 Could not fix: %s", issue.message)))
				fmt.Fprintln(session.Out, styles.Detail.Render(fmt.Sprintf("    \u2192 %s (manual: %s)", err, issue.fix)))
				failed++
				continue
			}
			fixed++
			if issue.residualHint == "" {
				fmt.Fprintln(session.Out, styles.Success.Render("\u2713 Fixed: "+issue.message))
			} else {
				fmt.Fprintln(session.Out, styles.Success.Render("\u2713 Fixed (partly): "+issue.message))
				fmt.Fprintln(session.Out, styles.Detail.Render("    \u2192 still needed: "+issue.residualHint))
				needsAttention++
			}
		}
	}
	if !anyIssue {
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Success.Render("\u2713 Nothing to fix."))
		return nil
	}

	fmt.Fprintln(session.Out)
	fmt.Fprintln(session.Out, styles.Header.Render(fmt.Sprintf(
		"%d fixed, %d still need attention, %d failed to fix.", fixed, needsAttention, failed,
	)))
	if needsAttention > 0 || failed > 0 {
		return fmt.Errorf("%d issue(s) still need attention", needsAttention+failed)
	}
	return nil
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
				v := v // per-iteration copy for the closure below (Go >=1.22 already does this automatically, but explicit here for clarity)
				issues = append(issues, doctorIssue{
					message: fmt.Sprintf("%s %s is registered, but its real target no longer exists", tool.DisplayName, v.Number),
					fix:     fmt.Sprintf("run `sk remove %s %s` to clear the stale registration", tool.Name, v.Number),
					// Safe to fully automate: this only ever removes
					// the dangling SYMLINK itself (v.External), never
					// the real files it used to point at -- same
					// guarantee removeVersion already documents.
					autofix: func() error { return removeVersion(v) },
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
				v := v
				issues = append(issues, doctorIssue{
					message: fmt.Sprintf("%s %s looks incomplete (missing or empty bin directory)", tool.DisplayName, v.Number),
					fix:     fmt.Sprintf("run `sk remove %s %s` and reinstall it", tool.Name, v.Number),
					// Only the removal half is safe to automate --
					// reinstalling needs a network call and is exactly
					// the kind of action `sk doctor fix` should never
					// take without being asked (see install.go's own
					// download).
					autofix:      func() error { return removeVersion(v) },
					residualHint: fmt.Sprintf("reinstall it: `sk install %s %s`", tool.Name, v.Number),
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
			tool := tool
			issues = append(issues, doctorIssue{
				message: fmt.Sprintf("%s's default (%s) no longer exists", tool.DisplayName, stored),
				fix:     fmt.Sprintf("run `sk default %s <version>` to set a new one, or `sk default %s null` to clear it", tool.Name, tool.Name),
				// Clearing a default that points nowhere is always
				// safe; CHOOSING a new one is a judgment call `sk
				// doctor fix` can't make for you.
				autofix:      func() error { return clearDefaultFile(tool) },
				residualHint: fmt.Sprintf("set a new default if you want one: `sk default %s <version>`", tool.Name),
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
		path := filepath.Join(tooldef.TempRoot(), e.Name())
		issues = append(issues, doctorIssue{
			warning: true,
			message: fmt.Sprintf("%s", path),
			fix:     fmt.Sprintf("safe to delete: rm -rf %s", path),
			autofix: func() error { return os.RemoveAll(path) },
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

// doctorFixResultJSON is one issue's outcome in `sk doctor fix`'s
// --format=json payload. Exactly one of Residual/Error is set:
// Residual on a fixed-but-not-fully-resolved issue (mirrors
// runDoctorFix's own "Fixed (partly)" case), Error if autofix itself
// failed (e.g. a permissions problem) -- in which case Fixed is
// false and the issue's full manual instructions are NOT repeated
// here (a caller already has them from `sk doctor --format=json`, and
// the design doc's own #9 rule -- exact, round-trippable data, not
// duplicated prose -- argues against re-sending them).
type doctorFixResultJSON struct {
	Check    string `json:"check"`
	Message  string `json:"message"`
	Fixed    bool   `json:"fixed"`
	Residual string `json:"residual,omitempty"`
	Error    string `json:"error,omitempty"`
}

type doctorFixSummaryJSON struct {
	Fixed          int `json:"fixed"`
	NeedsAttention int `json:"needsAttention"`
	Failed         int `json:"failed"`
}

type doctorFixData struct {
	Results []doctorFixResultJSON `json:"results"`
	Summary doctorFixSummaryJSON  `json:"summary"`
}

// buildDoctorFixJSON mirrors runDoctorFix exactly -- same four
// checkX() calls, same per-issue autofix()/residualHint handling --
// so the JSON and plain-text outcomes (including what actually got
// fixed on disk) can never disagree. Genuinely performs each autofix;
// this is not a dry run.
func buildDoctorFixJSON() *doctorFixData {
	groups := []struct {
		check  string
		issues []doctorIssue
	}{
		{"dangling_registrations", checkDanglingRegistrations()},
		{"incomplete_installs", checkIncompleteInstalls()},
		{"stale_defaults", checkStaleDefaults()},
		{"leftover_temp_dirs", checkLeftoverTempDirs()},
	}

	results := []doctorFixResultJSON{}
	var summary doctorFixSummaryJSON
	for _, g := range groups {
		for _, issue := range g.issues {
			if issue.autofix == nil {
				results = append(results, doctorFixResultJSON{Check: g.check, Message: issue.message, Fixed: false, Residual: issue.fix})
				summary.NeedsAttention++
				continue
			}
			if err := issue.autofix(); err != nil {
				results = append(results, doctorFixResultJSON{Check: g.check, Message: issue.message, Fixed: false, Error: err.Error()})
				summary.Failed++
				continue
			}
			r := doctorFixResultJSON{Check: g.check, Message: issue.message, Fixed: true}
			if issue.residualHint != "" {
				r.Residual = issue.residualHint
				summary.NeedsAttention++
			}
			results = append(results, r)
			summary.Fixed++
		}
	}
	return &doctorFixData{Results: results, Summary: summary}
}

// emitDoctorFixJSON mirrors emitDoctorJSON's own pattern: the
// envelope is always status "ok" (fixing what it can and reporting
// the rest is the successful outcome of this command, not a failure),
// with a separate *CLIError -- ErrCodeDoctorFixIncomplete, exit 201,
// shared with ErrCodeDoctorCheckFailed's own "still needs attention"
// exit code -- only when something remains unfixed or failed to fix.
func emitDoctorFixJSON(data *doctorFixData) error {
	writeJSONEnvelope(jsonEnvelope{SchemaVersion: schemaVersion, Status: "ok", Data: data})
	if data.Summary.NeedsAttention > 0 || data.Summary.Failed > 0 {
		return &CLIError{Code: ErrCodeDoctorFixIncomplete, Err: fmt.Errorf("%d issue(s) still need attention", data.Summary.NeedsAttention+data.Summary.Failed)}
	}
	return nil
}

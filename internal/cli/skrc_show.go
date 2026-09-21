package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"sdkkeeper/internal/inventory"
	"sdkkeeper/internal/tooldef"
)

// newSkrcCmd is `sk skrc` (bare, no subcommand): a read-only report of
// what `sk use` (0 args) would apply from here, and what state each
// pinned candidate is actually in right now. Never mutates anything --
// applying is still exclusively `sk use`'s job; this command exists so
// that fact can be checked without running it.
func newSkrcCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "skrc",
		Short: "Show what the nearest .skrc would apply from here",
		Example: `  sk skrc   # shows the path found, each entry, and installed/active status
  sk use    # actually applies it
  sk init skrc   # create one from what's currently active
  sk remove skrc # delete it`,
		Args: requireArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			if outputFormat == FormatJSON {
				data, jerr := buildSkrcJSON()
				return emitJSON(data, jerr)
			}
			return runShowSkrc()
		},
	}
}

// skrcReportEntry is one .skrc line's computed status -- the single
// source of truth both runShowSkrc's plain text and buildSkrcJSON's
// payload render from, so the two can never disagree (same principle
// as list/current sharing findActiveVersion).
type skrcReportEntry struct {
	Tool      string
	Version   string
	Known     bool // false if the tool name isn't in tooldef.Registry at all
	Installed bool
	Active    bool
}

// computeSkrcEntries resolves each parsed .skrc line against real,
// on-disk state: whether the tool is even recognized, whether the
// pinned version is installed, and whether it's the one currently
// active in THIS shell (same real-env-var check current/list use, via
// findActiveVersion). An unrecognized tool name or a scan failure
// never aborts the whole report -- each entry is independent, and a
// bad one just reports Known=false / Installed=false rather than
// failing the command (this is a report, not a validation).
func computeSkrcEntries(entries []skrcEntry) []skrcReportEntry {
	out := make([]skrcReportEntry, 0, len(entries))
	for _, e := range entries {
		tool, ok := tooldef.Get(e.Tool)
		if !ok {
			out = append(out, skrcReportEntry{Tool: e.Tool, Version: e.Version})
			continue
		}

		row := skrcReportEntry{Tool: e.Tool, Version: e.Version, Known: true}
		if versions, scanErr := inventory.Scan(tool); scanErr == nil {
			if _, ok := inventory.Find(tool, e.Version); ok {
				row.Installed = true
			}
			if tool.EnvVar != "" {
				if envVal, isSet := os.LookupEnv(tool.EnvVar); isSet {
					if v, ok := findActiveVersion(tool, versions, envVal); ok && v.Number == e.Version {
						row.Active = true
					}
				}
			}
		}
		out = append(out, row)
	}
	return out
}

func runShowSkrc() error {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf("\u2717 could not determine current directory: %s", err)))
		return err
	}

	path, found, err := findSkrc(cwd)
	if err != nil {
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf("\u2717 could not read .skrc: %s", err)))
		return err
	}
	if !found {
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Neutral.Render("No .skrc found between here and $HOME."))
		return nil
	}

	parsed, err := parseSkrc(path)
	if err != nil {
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf("\u2717 could not parse %s: %s", path, err)))
		return err
	}

	fmt.Fprintln(session.Out)
	fmt.Fprintln(session.Out, styles.Header.Render(fmt.Sprintf(".skrc found at %s", path)))
	if len(parsed) == 0 {
		fmt.Fprintln(session.Out, styles.Neutral.Render("  (no candidates listed)"))
		return nil
	}
	fmt.Fprintln(session.Out)

	entries := computeSkrcEntries(parsed)

	// Column widths computed once across every entry, same
	// fixed-width-padding approach list.go's own printVersions uses
	// and the same reason: consistent alignment across several
	// separately-formatted lines, not lipgloss/table's per-call
	// redistribution.
	toolWidth, versionWidth := 0, 0
	for _, e := range entries {
		if len(e.Tool) > toolWidth {
			toolWidth = len(e.Tool)
		}
		if len(e.Version) > versionWidth {
			versionWidth = len(e.Version)
		}
	}

	for _, e := range entries {
		if !e.Known {
			fmt.Fprintln(session.Out, styles.Error.Render(
				fmt.Sprintf("  \u2717 %-*s %-*s unknown tool", toolWidth, e.Tool, versionWidth, e.Version),
			))
			continue
		}

		glyph, style, status := "\u2717", styles.Error, "not installed"
		if e.Installed {
			glyph, style = "\u2713", styles.Success
			if e.Active {
				status = "installed, active"
			} else {
				status = "installed, not active"
			}
		}
		fmt.Fprintln(session.Out, style.Render(
			fmt.Sprintf("  %s %-*s %-*s %s", glyph, toolWidth, e.Tool, versionWidth, e.Version, status),
		))
	}

	return nil
}

// skrcEntryPayload/skrcData are skrc's --format=json success shape.
// Not yet part of the frozen design doc (that document predates this
// command entirely) -- follows its conventions anyway: a flat array
// like list's own `installed`, booleans rather than free-form status
// strings, and status "ok" even when individual entries report
// Installed=false/Known=false -- a missing or unrecognized candidate
// is a finding this report surfaces, not an invocation failure (same
// principle as doctor's per-check `fail` vs. the envelope's own
// status).
type skrcEntryPayload struct {
	Tool      string `json:"tool"`
	Version   string `json:"version"`
	Known     bool   `json:"known"`
	Installed bool   `json:"installed"`
	Active    bool   `json:"active"`
}

// Path is a pointer so "no .skrc found" serializes as JSON null, a
// valid non-error state -- same pattern current's own `active` field
// uses for "nothing active".
type skrcData struct {
	Path    *string            `json:"path"`
	Entries []skrcEntryPayload `json:"entries"`
}

// buildSkrcJSON mirrors runShowSkrc exactly (same findSkrc/parseSkrc/
// computeSkrcEntries calls), so the JSON and plain-text reports can
// never disagree with each other. Genuine invocation problems --
// can't determine cwd, can't read/parse the file -- are
// ErrCodeInternalError; "no .skrc found" and any per-entry
// unknown/not-installed/not-active finding are all still status "ok".
func buildSkrcJSON() (*skrcData, *jsonError) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, &jsonError{Code: ErrCodeInternalError, Message: err.Error()}
	}

	path, found, err := findSkrc(cwd)
	if err != nil {
		return nil, &jsonError{Code: ErrCodeInternalError, Message: err.Error()}
	}
	if !found {
		return &skrcData{Path: nil, Entries: []skrcEntryPayload{}}, nil
	}

	parsed, err := parseSkrc(path)
	if err != nil {
		return nil, &jsonError{Code: ErrCodeInternalError, Message: err.Error()}
	}

	rows := computeSkrcEntries(parsed)
	payload := make([]skrcEntryPayload, 0, len(rows))
	for _, r := range rows {
		payload = append(payload, skrcEntryPayload{
			Tool: r.Tool, Version: r.Version, Known: r.Known, Installed: r.Installed, Active: r.Active,
		})
	}

	return &skrcData{Path: &path, Entries: payload}, nil
}

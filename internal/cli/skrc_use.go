package cli

import (
	"fmt"
	"os"
	"sort"

	"sdkkeeper/internal/inventory"
	"sdkkeeper/internal/tooldef"
)

// runUseFromSkrc implements `sk use` with no arguments: find and
// apply the nearest .skrc instead of requiring an explicit tool name.
//
// Deliberately NOT routed through resolveUse. Every .skrc entry
// already names an exact version, so there is no picker to show and
// no interactive TTY question to ask -- resolveUse's picker/
// RequiresJava-auto-chain logic solves a different, single-tool,
// possibly-version-less problem. Reusing it here would risk silently
// launching a picker mid-batch the moment a RequiresJava tool (e.g.
// Maven) is processed before JAVA_HOME is visible in this process,
// which is exactly the kind of surprise this feature exists to avoid.
// Instead this does the same low-level activation resolveUse's own
// success path does (inventory.Find + Result), directly.
func runUseFromSkrc() error {
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
		fmt.Fprintln(session.Out, styles.Error.Render(
			"\u2717 sk use requires a tool name (e.g. sk use java), or a .skrc file in this directory or a parent\n"+
				"usage: sk use [<tool> [version|null]]",
		))
		return fmt.Errorf("tool name is missing")
	}

	entries, err := parseSkrc(path)
	if err != nil {
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf("\u2717 could not parse %s: %s", path, err)))
		return err
	}
	if len(entries) == 0 {
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Neutral.Render(fmt.Sprintf("%s has no candidates to activate.", path)))
		return nil
	}

	// Prerequisite tools (currently just java, via RequiresJava) are
	// applied first regardless of file order, so a later dependent
	// entry in this same batch sees JAVA_HOME already set -- see the
	// os.Setenv call below, which is what makes that visible within
	// this one process.
	sort.SliceStable(entries, func(i, j int) bool {
		ti, _ := tooldef.Get(entries[i].Tool)
		tj, _ := tooldef.Get(entries[j].Tool)
		return !ti.RequiresJava && tj.RequiresJava
	})

	format := parseShellFormat(shellFormatFlag)
	fmt.Fprintln(session.Out)
	anyFailed := false

	for _, e := range entries {
		tool, ok := tooldef.Get(e.Tool)
		if !ok {
			fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf("\u2717 %s %s — unknown tool (.skrc)", e.Tool, e.Version)))
			anyFailed = true
			continue
		}

		if tool.RequiresJava {
			if _, alreadySet := os.LookupEnv("JAVA_HOME"); !alreadySet {
				fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf(
					"\u2717 %s %s — needs a JDK, but none is active and .skrc has no java entry (.skrc)", tool.DisplayName, e.Version,
				)))
				anyFailed = true
				continue
			}
		}

		v, ok := inventory.Find(tool, e.Version)
		if !ok {
			fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf("\u2717 %s %s — not installed (.skrc)", tool.DisplayName, e.Version)))
			anyFailed = true
			continue
		}

		result := newResult()
		if tool.EnvVar != "" {
			result.EnvVars[tool.EnvVar] = tool.HomePath(v.Path)
		}
		result.PathPrepends = append(result.PathPrepends, PathPrepend{
			Dir:         tool.BinPath(v.Path),
			StripPrefix: tool.CandidateRoot(),
		})
		writeResult(result, format)

		// Visible to a later entry's RequiresJava check above, within
		// this same process -- resolveUse's equivalent check only
		// ever sees this after the shell wrapper applies writeResult's
		// output post-exit, which is too late for this same batch.
		if tool.EnvVar != "" {
			os.Setenv(tool.EnvVar, tool.HomePath(v.Path))
		}

		fmt.Fprintln(session.Out, styles.Success.Render(fmt.Sprintf(
			"\u2713 %s %s — active for this shell session only (.skrc)", tool.DisplayName, e.Version,
		)))
	}

	if anyFailed {
		return fmt.Errorf("one or more .skrc candidates failed to activate")
	}
	return nil
}

// skrcOverride looks up toolName's pinned version in the nearest
// .skrc, if any, and reports it alongside whether it differs from
// activatedVersion -- the version actually just activated by an
// explicit `sk use <tool> ...` call. Returns ok=false whenever there
// is nothing worth telling the user: no .skrc, no entry for this
// tool, or the entry already matches what was activated.
func skrcOverride(toolName, activatedVersion string) (pinned string, ok bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	path, found, err := findSkrc(cwd)
	if err != nil || !found {
		return "", false
	}
	entries, err := parseSkrc(path)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if e.Tool == toolName {
			return e.Version, e.Version != activatedVersion
		}
	}
	return "", false
}

// skrcUseResult is one .skrc candidate's outcome in `sk use`'s
// --format=json batch payload: either Use (the same usePayload shape
// a single-entry `sk use <tool> <version> --format=json` call
// returns, giving a caller everything needed to apply it itself,
// since --format=json can never mutate the calling shell) or Error,
// never both.
type skrcUseResult struct {
	Tool    string      `json:"tool"`
	Version string      `json:"version"`
	Success bool        `json:"success"`
	Use     *usePayload `json:"use,omitempty"`
	Error   *jsonError  `json:"error,omitempty"`
}

type skrcUseSummary struct {
	Activated int `json:"activated"`
	Failed    int `json:"failed"`
}

type skrcUseData struct {
	Path    string          `json:"path"`
	Results []skrcUseResult `json:"results"`
	Summary skrcUseSummary  `json:"summary"`
}

// buildSkrcUseJSON is the --format=json counterpart to
// runUseFromSkrc: same discovery (findSkrc), same parsing
// (parseSkrc), same java-first ordering, and -- critically -- each
// candidate resolved via resolveUseJSON, the SAME per-tool resolution
// `sk use <tool> <version> --format=json` already uses. That reuse is
// what makes this correct rather than a second, subtly-divergent
// implementation: unknown-tool, not-found, and the RequiresJava/
// JAVA_HOME guard all come from resolveUseJSON's own real-environment
// checks, including the os.Setenv trick below making an
// earlier-in-this-batch java entry visible to a later RequiresJava
// one -- resolveUseJSON only ever sees real process env, so without
// this a later entry would see the SAME "JAVA_HOME not set" failure
// runUseFromSkrc's own guard exists to report explicitly.
//
// Only genuine invocation failures (can't determine cwd, can't read/
// parse the file, no .skrc found at all) are returned as *jsonError
// here. A per-candidate failure is a batch finding, not an invocation
// error -- see emitSkrcUseJSON, which is what turns Summary.Failed>0
// into the process's own non-zero exit code, while the envelope
// itself still reports status "ok" (same principle as
// ErrCodeDoctorCheckFailed).
func buildSkrcUseJSON() (*skrcUseData, *jsonError) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, &jsonError{Code: ErrCodeInternalError, Message: err.Error()}
	}

	path, found, err := findSkrc(cwd)
	if err != nil {
		return nil, &jsonError{Code: ErrCodeInternalError, Message: err.Error()}
	}
	if !found {
		return nil, &jsonError{Code: ErrCodeSkrcNotFound, Message: "no .skrc found between here and $HOME"}
	}

	entries, err := parseSkrc(path)
	if err != nil {
		return nil, &jsonError{Code: ErrCodeInternalError, Message: err.Error()}
	}

	sort.SliceStable(entries, func(i, j int) bool {
		ti, _ := tooldef.Get(entries[i].Tool)
		tj, _ := tooldef.Get(entries[j].Tool)
		return !ti.RequiresJava && tj.RequiresJava
	})

	results := make([]skrcUseResult, 0, len(entries))
	activated, failed := 0, 0
	for _, e := range entries {
		payload, jerr := resolveUseJSON(e.Tool, e.Version)
		if jerr != nil {
			results = append(results, skrcUseResult{Tool: e.Tool, Version: e.Version, Success: false, Error: jerr})
			failed++
			continue
		}
		if payload.EnvVar != "" {
			os.Setenv(payload.EnvVar, payload.EnvValue)
		}
		results = append(results, skrcUseResult{Tool: e.Tool, Version: e.Version, Success: true, Use: payload})
		activated++
	}

	return &skrcUseData{Path: path, Results: results, Summary: skrcUseSummary{Activated: activated, Failed: failed}}, nil
}

// emitSkrcUseJSON mirrors emitDoctorJSON's own pattern: writes the
// envelope directly (always status "ok" once buildSkrcUseJSON itself
// succeeded), then separately returns a *CLIError -- carrying
// ErrCodeSkrcBatchPartialFailure's exit code -- only if at least one
// candidate failed, so the process's own exit code still reflects
// that without touching the JSON body's own status field.
func emitSkrcUseJSON() error {
	data, jerr := buildSkrcUseJSON()
	if jerr != nil {
		return emitJSON(nil, jerr)
	}
	writeJSONEnvelope(jsonEnvelope{SchemaVersion: schemaVersion, Status: "ok", Data: data})
	if data.Summary.Failed > 0 {
		return &CLIError{Code: ErrCodeSkrcBatchPartialFailure, Err: fmt.Errorf("%d of %d .skrc candidate(s) failed to activate", data.Summary.Failed, len(data.Results))}
	}
	return nil
}

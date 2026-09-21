package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sdkkeeper/internal/inventory"
	"sdkkeeper/internal/tooldef"
)

// skrcHomePath is $HOME/.skrc -- the one fixed location `sk init skrc`
// writes to and `sk remove skrc` deletes. Distinct from findSkrc's
// walk-up lookup (used by `sk use` and the override-warning check),
// which reads whichever .skrc it finds nearest to cwd -- not
// necessarily this one, if cwd is a project directory elsewhere under
// $HOME with its own .skrc closer by.
func skrcHomePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, skrcFileName), nil
}

// snapshotActiveSkrcEntries reads every tool that's currently active
// in the CALLING shell (real *_HOME env vars, as this process sees
// them -- never the persisted `default`, which is a different,
// possibly-stale preference, not "what's active right now") and
// returns one skrcEntry per genuinely resolvable one.
//
// The single source of truth for BOTH runInitSkrc's plain text and
// buildInitSkrcJSON's payload -- same principle as
// computeSkrcEntries/skrc_show.go, so the two can never disagree.
func snapshotActiveSkrcEntries() []skrcEntry {
	// Sorted for deterministic output -- Registry is a map, iteration
	// order is otherwise random.
	names := make([]string, 0, len(tooldef.Registry))
	for name := range tooldef.Registry {
		names = append(names, name)
	}
	sort.Strings(names)

	var entries []skrcEntry
	for _, name := range names {
		tool := tooldef.Registry[name]
		if tool.EnvVar == "" {
			continue
		}
		current, isSet := os.LookupEnv(tool.EnvVar)
		if !isSet || current == "" {
			continue
		}
		versions, scanErr := inventory.Scan(tool)
		if scanErr != nil {
			// One tool's unreadable candidates directory shouldn't
			// abort the whole snapshot -- skip it, same "verify or
			// skip, never guess" instinct as the unmatched-value case
			// below.
			continue
		}
		v, ok := findActiveVersion(tool, versions, current)
		if !ok {
			// Env var is set but doesn't match any version sk
			// recognizes -- stale, or hand-edited. Silently skipped,
			// matching `sk current`'s own treatment of the same case
			// (reported as "not currently active", not an error).
			continue
		}
		entries = append(entries, skrcEntry{Tool: tool.Name, Version: v.Number})
	}
	return entries
}

// renderSkrcContent turns entries into the exact bytes runInitSkrc
// writes to disk -- one tool=version line each, or a single '#'
// placeholder comment when there's nothing to snapshot (so the empty
// file still parses cleanly via parseSkrc, and reads as deliberate
// rather than a truncated/broken write).
func renderSkrcContent(entries []skrcEntry) string {
	if len(entries) == 0 {
		return "# No tools were active when this was generated — sk init skrc\n"
	}
	lines := make([]string, len(entries))
	for i, e := range entries {
		lines[i] = fmt.Sprintf("%s=%s", e.Tool, e.Version)
	}
	return strings.Join(lines, "\n") + "\n"
}

// runInitSkrc implements `sk init skrc`. Refuses outright if one
// already exists at $HOME/.skrc -- overwriting a real, possibly
// hand-edited pin file would be exactly the kind of implicit mutation
// sk avoids everywhere else (same instinct behind "no implicit
// default"). `sk remove skrc` then `sk init skrc` is the explicit way
// to regenerate.
func runInitSkrc() error {
	path, err := skrcHomePath()
	if err != nil {
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf("\u2717 could not determine home directory: %s", err)))
		return err
	}

	if _, statErr := os.Stat(path); statErr == nil {
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf(
			"\u2717 %s already exists — not touching it (sk remove skrc, then sk init skrc, to regenerate)", path,
		)))
		return fmt.Errorf(".skrc already exists at %s", path)
	} else if !os.IsNotExist(statErr) {
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf("\u2717 could not check %s: %s", path, statErr)))
		return statErr
	}

	entries := snapshotActiveSkrcEntries()
	content := renderSkrcContent(entries)

	if writeErr := os.WriteFile(path, []byte(content), 0o644); writeErr != nil {
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf("\u2717 could not write %s: %s", path, writeErr)))
		return writeErr
	}

	fmt.Fprintln(session.Out)
	if len(entries) == 0 {
		fmt.Fprintln(session.Out, styles.Success.Render(fmt.Sprintf("\u2713 Created %s (no tools were active — empty)", path)))
		return nil
	}
	fmt.Fprintln(session.Out, styles.Success.Render(fmt.Sprintf("\u2713 Created %s with %d candidate(s):", path, len(entries))))
	for _, e := range entries {
		fmt.Fprintln(session.Out, styles.Detail.Render(fmt.Sprintf("  \u2514\u2500 %s=%s", e.Tool, e.Version)))
	}
	return nil
}

// runRemoveSkrc implements `sk remove skrc`: delete $HOME/.skrc.
// Always the fixed $HOME location, matching runInitSkrc -- not a
// walk-up delete of whichever .skrc a project directory happens to
// have; that would risk deleting a file this command never created.
func runRemoveSkrc() error {
	path, err := skrcHomePath()
	if err != nil {
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf("\u2717 could not determine home directory: %s", err)))
		return err
	}

	if removeErr := os.Remove(path); removeErr != nil {
		if os.IsNotExist(removeErr) {
			fmt.Fprintln(session.Out)
			fmt.Fprintln(session.Out, styles.Neutral.Render(fmt.Sprintf("%s does not exist — nothing to remove.", path)))
			return nil
		}
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Error.Render(fmt.Sprintf("\u2717 could not remove %s: %s", path, removeErr)))
		return removeErr
	}

	fmt.Fprintln(session.Out)
	fmt.Fprintln(session.Out, styles.Success.Render(fmt.Sprintf("\u2713 Removed %s", path)))
	return nil
}

// skrcInitEntryPayload/skrcInitData are `sk init skrc`'s
// --format=json success shape.
type skrcInitEntryPayload struct {
	Tool    string `json:"tool"`
	Version string `json:"version"`
}

type skrcInitData struct {
	Path    string                 `json:"path"`
	Entries []skrcInitEntryPayload `json:"entries"`
}

// buildInitSkrcJSON mirrors runInitSkrc exactly (same skrcHomePath/
// snapshotActiveSkrcEntries/renderSkrcContent calls), so the JSON and
// plain-text outcomes -- including the file actually written to disk
// -- can never disagree.
func buildInitSkrcJSON() (*skrcInitData, *jsonError) {
	path, err := skrcHomePath()
	if err != nil {
		return nil, &jsonError{Code: ErrCodeInternalError, Message: err.Error()}
	}

	if _, statErr := os.Stat(path); statErr == nil {
		return nil, &jsonError{Code: ErrCodeSkrcAlreadyExists, Message: fmt.Sprintf("%s already exists", path)}
	} else if !os.IsNotExist(statErr) {
		return nil, &jsonError{Code: ErrCodeInternalError, Message: statErr.Error()}
	}

	entries := snapshotActiveSkrcEntries()
	content := renderSkrcContent(entries)
	if writeErr := os.WriteFile(path, []byte(content), 0o644); writeErr != nil {
		return nil, &jsonError{Code: ErrCodeInternalError, Message: writeErr.Error()}
	}

	payload := make([]skrcInitEntryPayload, len(entries))
	for i, e := range entries {
		payload[i] = skrcInitEntryPayload{Tool: e.Tool, Version: e.Version}
	}
	return &skrcInitData{Path: path, Entries: payload}, nil
}

// skrcRemoveData is `sk remove skrc`'s --format=json success shape.
// Action is a closed, two-value enum -- "removed" when a real file
// was deleted, "not_present" when there was nothing to remove; the
// latter is still status "ok", matching the plain-text path's own
// "already gone is not an error" rule.
type skrcRemoveData struct {
	Path   string `json:"path"`
	Action string `json:"action"`
}

func buildRemoveSkrcJSON() (*skrcRemoveData, *jsonError) {
	path, err := skrcHomePath()
	if err != nil {
		return nil, &jsonError{Code: ErrCodeInternalError, Message: err.Error()}
	}

	if removeErr := os.Remove(path); removeErr != nil {
		if os.IsNotExist(removeErr) {
			return &skrcRemoveData{Path: path, Action: "not_present"}, nil
		}
		return nil, &jsonError{Code: ErrCodeInternalError, Message: removeErr.Error()}
	}
	return &skrcRemoveData{Path: path, Action: "removed"}, nil
}

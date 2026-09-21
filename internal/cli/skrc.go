package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// skrcFileName is the project-level, multi-tool activation file --
// named to match the `sk` command itself (matching the reasoning that
// gave `sk` its own short name over the longer `sdkkeeper` module),
// not `.sdkkeeperrc`. Deliberately NOT auto-applied on `cd`: sk's
// founding "no implicit default, ever" principle covers this file
// too. It only takes effect when the user explicitly runs `sk use`
// with no arguments -- see runUseFromSkrc in skrc_use.go.
const skrcFileName = ".skrc"

// skrcEntry is one `tool=version` line.
type skrcEntry struct {
	Tool    string
	Version string
}

// findSkrc walks up from dir looking for a .skrc file, the same
// walk-up convention as git's own config discovery -- bounded at
// $HOME, not the filesystem root. Without a stop, a stray .skrc left
// somewhere above a project (accidentally, or in a shared/home
// directory) would silently apply to every project under it; $HOME is
// the natural boundary of "inside a project" for sk's purposes.
// Returns the full path and true if found; ("", false, nil) if none
// exists between dir and that boundary -- not an error, just the
// common case. If dir itself isn't under $HOME (or $HOME can't be
// determined), the walk still terminates at the filesystem root as a
// fallback, so this never loops forever.
func findSkrc(dir string) (string, bool, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", false, err
	}
	home, _ := os.UserHomeDir() // "" if undeterminable -- boundary below then never matches, falls through to the root stop
	for {
		candidate := filepath.Join(dir, skrcFileName)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true, nil
		} else if err != nil && !os.IsNotExist(err) {
			return "", false, err
		}
		if home != "" && dir == home {
			return "", false, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false, nil
		}
		dir = parent
	}
}

// parseSkrc reads path as `tool=version` lines, one candidate per
// line -- blank lines and lines starting with `#` are skipped.
// "default" and "null" are rejected as versions: both are reserved
// tokens meaningful only to `sk use <tool> <arg>`'s own argument slot,
// and a project pin should always be an exact, literal version (see
// the design doc's §9 round-trippable-version rule).
func parseSkrc(path string) ([]skrcEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []skrcEntry
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		tool, version, ok := strings.Cut(line, "=")
		tool = strings.TrimSpace(tool)
		version = strings.TrimSpace(version)
		if !ok || tool == "" || version == "" {
			return nil, fmt.Errorf("%s:%d: expected tool=version, got %q", filepath.Base(path), lineNum, line)
		}
		if version == "default" || version == "null" {
			return nil, fmt.Errorf("%s:%d: %s must pin an exact version, not %q", filepath.Base(path), lineNum, tool, version)
		}
		entries = append(entries, skrcEntry{Tool: tool, Version: version})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// Package inventory scans the filesystem for installed versions of a
// tool. Deliberately pure: no TTY, no terminal, no interactive I/O
// anywhere in this package -- only os.ReadDir and string handling, so
// this logic can be unit tested directly, unlike the picker/term
// packages, which cannot be meaningfully tested outside a real terminal
// (see design doc §6.4 for exactly why that distinction matters).
package inventory

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"sdkkeeper/internal/tooldef"
)

// Version is one discovered, installed version of a tool.
type Version struct {
	// Number is the version string as it appears in the folder name,
	// e.g. "21.0.2" -- prefix already stripped.
	Number string

	// Path is the full, real filesystem path to this version's folder
	// (i.e. the versionDir that tooldef.Tool.HomePath/BinPath expect).
	// If this entry is a symlink (see External below), Path is the
	// symlink's OWN path, not its resolved target -- the OS follows
	// the symlink transparently whenever this path is actually used
	// (setting JAVA_HOME to it, running a binary under it), so there's
	// no need to resolve it here.
	Path string

	// External is true if this entry is a symlink rather than a real
	// directory -- meaning it was registered via `add` (pointing at
	// wherever the real install actually lives) rather than placed
	// there by `install`. Surfaced so callers (e.g. `list`) can label
	// them differently, without inventory needing to know anything
	// about display formatting itself.
	External bool
}

// Scan returns every installed version of t under its sdkkeeper-managed
// CandidateRoot, sorted newest-first. If CandidateRoot doesn't exist
// yet (nothing has ever been installed or added for this tool), that's
// treated as zero versions, not an error -- the normal, expected case
// the first time a tool is used.
func Scan(t tooldef.Tool) ([]Version, error) {
	versions, err := scanRoot(t.CandidateRoot(), t.FolderPrefix)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	sort.SliceStable(versions, func(i, j int) bool {
		return CompareVersions(versions[i].Number, versions[j].Number) > 0
	})

	return versions, nil
}

func scanRoot(root, prefix string) ([]Version, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	re := regexp.MustCompile("^" + regexp.QuoteMeta(prefix) + "(.+)$")
	var versions []Version
	for _, e := range entries {
		isSymlink := e.Type()&os.ModeSymlink != 0
		// DirEntry.IsDir() checks the entry's OWN mode bits, which is
		// false for a symlink even when it points at a real directory
		// -- so a symlink must be explicitly allowed here too, or
		// every `add`-ed entry would be silently skipped entirely.
		if !e.IsDir() && !isSymlink {
			continue
		}
		m := re.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		versions = append(versions, Version{
			Number:   m[1],
			Path:     filepath.Join(root, e.Name()),
			External: isSymlink,
		})
	}
	return versions, nil
}

// Find looks up a single specific version by number. Returns ok=false
// if not installed/registered.
func Find(t tooldef.Tool, number string) (Version, bool) {
	versions, err := Scan(t)
	if err != nil {
		return Version{}, false
	}
	for _, v := range versions {
		if v.Number == number {
			return v, true
		}
	}
	return Version{}, false
}

// Numbers extracts just the version strings from a Scan result, e.g.
// for feeding directly into the picker, which only needs strings.
func Numbers(versions []Version) []string {
	out := make([]string, len(versions))
	for i, v := range versions {
		out[i] = v.Number
	}
	return out
}

// FormatNotFound builds the "not found, here's what IS available"
// message used consistently across every tool's `use`/`remove` error
// path (design doc: the print-not-found consolidation from the
// shell-script version, ported here as the one shared implementation).
// action names what was being attempted (e.g. "use", "remove") --
// included directly in the message so it's unambiguous on its own,
// even scrolled back to or seen without the original command visible
// (a real gap found via actual use: a bare "not found" said WHAT
// didn't exist, but never WHY the user was looking for it).
// FormatNotFound builds the "not found, here's what IS available"
// message used consistently across every tool's `use`/`remove` error
// path (design doc: the print-not-found consolidation from the
// shell-script version, ported here as the one shared implementation).
// action names what was being attempted (e.g. "use", "remove") --
// included directly in the message so it's unambiguous on its own,
// even scrolled back to or seen without the original command visible
// (a real gap found via actual use: a bare "not found" said WHAT
// didn't exist, but never WHY the user was looking for it).
//
// displayName (e.g. "JDK", "Maven") is used ONLY for the empty-
// versions case -- a real gap found via actual use: with NOTHING
// installed or added at all, this used to print an "Available
// versions:" header followed by nothing at all underneath it, which
// reads as a possible display bug rather than a clear, honest "there
// is nothing" statement. Reuses the exact phrasing `list` already
// established for this same "nothing installed or added yet" state,
// rather than a differently-worded message for what is, from the
// user's perspective, the identical situation.
func FormatNotFound(prefix, version, action, displayName string, versions []Version) string {
	var b strings.Builder
	b.WriteString("\u2717 " + prefix + version + " not found — nothing to " + action + "\n")
	if len(versions) == 0 {
		b.WriteString("No " + displayName + " versions installed or added yet.")
		return b.String()
	}
	b.WriteString("Available versions:\n")
	for _, v := range versions {
		b.WriteString("  " + v.Number + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

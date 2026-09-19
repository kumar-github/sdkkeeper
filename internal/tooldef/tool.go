// Package tooldef defines the shape of a manageable SDK/tool (Java,
// Maven, Gradle, Node, Kafka, ...) as data, not code -- the variation
// between tools (folder names, binary layout, whether a JDK is
// required first) lives in a struct; the actual logic (inventory
// scanning, activation, removal) is written once and operates on
// whichever Tool it's given.
package tooldef

import (
	"os"
	"path/filepath"
	"runtime"
)

// Tool describes everything sdkkeeper needs to know to manage one kind of
// SDK, without hardcoding any tool-specific behavior elsewhere in the
// codebase.
type Tool struct {
	// Name is the tool identifier used on the command line, e.g. "java".
	Name string

	// DisplayName is the friendly, capitalized name used in user-facing
	// messages, e.g. "JDK" (not "java"), kept separate from Name since
	// the two often differ.
	DisplayName string

	// FolderPrefix is the version-folder naming prefix under this tool's
	// candidate root, e.g. "JDK-" (folders look like "JDK-21.0.2").
	FolderPrefix string

	// BinSubdir is the path, relative to a version folder, where the
	// bin directory lives -- "bin" for most tools. The macOS JDK
	// bundle layout is the exception, handled in BinPath/HomePath
	// instead of hardcoded here, so other tools don't go through
	// Java's OS-specific logic.
	BinSubdir string

	// EnvVar is the environment variable this tool's "home" path should
	// be exported as, e.g. "JAVA_HOME". Empty if the tool has no such
	// variable (some tools only need PATH updated, not a *_HOME var).
	EnvVar string

	// RequiresJava is true for tools that need an active JDK before
	// they can run (Maven, Gradle, Kafka).
	RequiresJava bool
}

// HomePath returns the directory that EnvVar should point at, for a
// version folder at versionDir -- where the macOS-only "Contents/Home"
// nesting is resolved.
//
// Probes the filesystem rather than assuming every vendor packages
// the same way: Temurin's macOS archives use the Apple-bundle
// structure, but Liberica's don't -- JAVA_HOME is the extracted
// directory itself, with no wrapper. Unconditionally appending
// "Contents/Home" for any java install on darwin would silently
// compute a JAVA_HOME with no real bin directory for Liberica.
// Checking whether it genuinely exists is also robust for whatever
// vendor comes next.
//
// Windows is not special-cased: Contents/Home is an Apple/macOS
// bundle convention, and neither vendor's Windows archives use an
// analogous wrapper (both show a flat top-level structure, like
// linux). Unverified against a real Windows archive, though -- if
// that assumption is wrong, a Windows-specific probe would go here,
// mirroring probeDarwinBundle.
func (t Tool) HomePath(versionDir string) string {
	if t.Name == "java" && runtime.GOOS == "darwin" {
		return probeDarwinBundle(versionDir)
	}
	return versionDir
}

// probeDarwinBundle checks whether versionDir/Contents/Home exists as
// a real directory, returning it if so, or versionDir unchanged if
// not. Extracted so this probing logic is testable regardless of
// which OS the test runs on -- HomePath's runtime.GOOS gate is what's
// actually OS-dependent, not this function.
func probeDarwinBundle(versionDir string) string {
	bundlePath := filepath.Join(versionDir, "Contents", "Home")
	if info, err := os.Stat(bundlePath); err == nil && info.IsDir() {
		return bundlePath
	}
	return versionDir
}

// BinPath returns the directory that should be prepended to PATH, for a
// version folder at versionDir.
func (t Tool) BinPath(versionDir string) string {
	subdir := t.BinSubdir
	if subdir == "" {
		subdir = "bin"
	}
	return filepath.Join(t.HomePath(versionDir), subdir)
}

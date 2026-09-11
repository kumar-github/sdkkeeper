// Package tooldef defines the shape of a manageable SDK/tool (Java, Maven,
// Gradle, Node, Kafka, ...) as data, not code. This is the fix for the
// exact problem the original shell-script prototype had: five nearly
// identical jdk-use/mvn-use/gradle-use/node-use/kafka-use implementations,
// differing only in folder names, binary layout, and whether a JDK is
// required first. Here, that variation lives in a struct; the actual
// logic (inventory scanning, activation, removal) is written once and
// operates on whichever Tool it's given.
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
	// messages, e.g. "JDK" (not "java") for a confirmation like
	// "JDK 21.0.2 — active for this shell session only". Kept separate
	// from Name since the command-line identifier and the natural way
	// to refer to a tool in a sentence often differ (nobody says "I
	// selected java 21.0.2" -- they say "I selected JDK 21.0.2").
	DisplayName string

	// FolderPrefix is the version-folder naming prefix under this tool's
	// candidate root, e.g. "JDK-" (folders look like "JDK-21.0.2").
	FolderPrefix string

	// BinSubdir is the path, relative to a version folder, where the
	// executable's "bin" directory lives. On most tools this is just
	// "bin". The macOS JDK bundle layout is the deliberate exception --
	// see BinPath below, which is why this is NOT simply hardcoded
	// "Contents/Home/bin" here: only Java needs that, and only on
	// Darwin. Keeping this field for the common (non-Java) case avoids
	// forcing every tool through Java's OS-specific logic.
	BinSubdir string

	// EnvVar is the environment variable this tool's "home" path should
	// be exported as, e.g. "JAVA_HOME". Empty if the tool has no such
	// variable (some tools only need PATH updated, not a *_HOME var).
	EnvVar string

	// RequiresJava is true for tools that need an active JDK before they
	// can run (Maven, Gradle, Kafka) -- drives the ensure-jdk-first
	// behavior already proven out in the original shell scripts.
	RequiresJava bool
}

// HomePath returns the directory that EnvVar should point at, for a
// version folder at versionDir. This is where the macOS-only
// "Contents/Home" nesting (design doc §11.5) is resolved properly,
// instead of being hardcoded inline the way the original PoC did it.
//
// Deliberately PROBES the filesystem rather than assuming every
// vendor packages the same way -- a real bug found via actual use on
// a real Mac: this used to unconditionally append "Contents/Home" for
// ANY java install on darwin, regardless of vendor. Temurin's own
// macOS archives DO use that Apple-bundle structure, but Liberica's
// do not -- confirmed directly from BellSoft's own official install
// documentation, which shows JAVA_HOME set to the extracted directory
// itself, with no Contents/Home wrapper at all, across every version
// checked (8.x through 25.x). The old, hardcoded logic computed a
// JAVA_HOME for Liberica that didn't actually contain a real `bin`
// directory -- so the shell silently fell through to whatever OTHER
// java happened to still be resolvable on PATH, with no error or
// warning that anything was wrong.
//
// Checking whether Contents/Home genuinely exists (rather than
// hardcoding per-vendor knowledge) is also more robust for whatever
// vendor comes next -- this needs no update if a future JDK vendor
// packages either way.
//
// Windows: deliberately NOT special-cased here -- Contents/Home is
// specifically an Apple/macOS application-bundle convention, and
// neither Temurin's nor Liberica's Windows archives are known to use
// any analogous nested wrapper (both vendors' own download URLs and
// published archive listings show a flat "jdk-X.Y.Z+B/bin,lib,..."
// top-level structure, the same shape linux already uses correctly
// with no special case at all). Still genuinely UNVERIFIED against a
// real, extracted Windows archive on a real machine, though -- the
// same honest, stated limitation as the rest of this project's
// Windows support -- so if that assumption turns out wrong, this is
// the exact spot a Windows-specific probe (mirroring
// probeDarwinBundle's own pattern) would need to be added.
func (t Tool) HomePath(versionDir string) string {
	if t.Name == "java" && runtime.GOOS == "darwin" {
		return probeDarwinBundle(versionDir)
	}
	return versionDir
}

// probeDarwinBundle checks whether versionDir/Contents/Home exists as
// a real directory, returning it if so, or versionDir unchanged if
// not. Extracted as its own function specifically so this probing
// logic can be tested directly regardless of which OS the test itself
// runs on -- the DECISION to call this at all is still gated by
// runtime.GOOS in HomePath above (untestable from a non-darwin
// sandbox, same as any compile-time OS check), but the probing logic
// itself has no OS dependency and is fully testable anywhere.
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

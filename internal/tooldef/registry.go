package tooldef

import (
	"os"
	"path/filepath"
)

// candidatesRoot is ~/.sdkkeeper/candidates -- see design doc §10.3.
// Kept unexported; callers get it through Tool.CandidateRoot() so the
// actual path construction lives in exactly one place.
func candidatesRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".sdkkeeper", "candidates")
}

// TempRoot is ~/.sdkkeeper/tmp -- scratch space for `install`'s
// download/extract cycle. Deliberately a sibling of candidates/, not
// system /tmp: installer.Options.TempRoot must be on the same
// filesystem as the final destination for the atomic os.Rename
// placement to actually be atomic (see internal/installer's package
// doc) -- system /tmp is very often a separate filesystem/tmpfs
// mount, which would silently break that guarantee.
func TempRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".sdkkeeper", "tmp")
}

// CandidateRoot returns the sdkkeeper-managed root for this tool, e.g.
// ~/.sdkkeeper/candidates/java. Every version this tool knows about
// lives directly under here -- either as a real directory (written by
// the future `install` command) or as a symlink (written by `add`,
// pointing at wherever the real install actually lives). There is no
// separate "legacy roots" concept anymore: a hardcoded, personal path
// like ~/MyApps/JAVA was never something a general tool should assume
// of anyone else's machine. `add` is the one, general mechanism for
// registering ANY existing install, not just ones under one
// specific, hardcoded location.
func (t Tool) CandidateRoot() string {
	return filepath.Join(candidatesRoot(), t.Name)
}

// DefaultPath returns the file path where this tool's remembered
// default version is stored, e.g. ~/.sdkkeeper/defaults/java -- a
// flat, one-line file containing just the version string (e.g.
// "21.0.2-temurin"). Deliberately a plain file, not a database or
// structured config, matching how every other piece of state in this
// tool is just the filesystem itself. Set via `sk default java
// <version>`, applied via `sk use java default` -- never written to
// automatically by any other command, so having a default set is
// always the result of one explicit, deliberate action, never a side
// effect of something else.
func (t Tool) DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".sdkkeeper", "defaults", t.Name)
}

// Registry is the full, current set of tools sdkkeeper knows how to
// manage. Adding a new tool means adding one entry here -- no new code
// elsewhere, per the whole point of this package.
var Registry = map[string]Tool{
	"java": {
		Name:         "java",
		DisplayName:  "JDK",
		FolderPrefix: "JDK-",
		EnvVar:       "JAVA_HOME",
		RequiresJava: false,
	},
	"maven": {
		Name:         "maven",
		DisplayName:  "Maven",
		FolderPrefix: "apache-maven-",
		EnvVar:       "MAVEN_HOME",
		RequiresJava: true,
	},
	"gradle": {
		Name:         "gradle",
		DisplayName:  "Gradle",
		FolderPrefix: "gradle-",
		EnvVar:       "GRADLE_HOME",
		RequiresJava: true,
	},
	"node": {
		Name:         "node",
		DisplayName:  "Node.js",
		FolderPrefix: "node-",
		EnvVar:       "NODE_HOME",
		RequiresJava: false,
	},
	"kafka": {
		Name:         "kafka",
		DisplayName:  "Kafka",
		FolderPrefix: "kafka-",
		EnvVar:       "KAFKA_HOME",
		RequiresJava: true,
	},
}

// Get looks up a tool by name. ok is false for an unrecognized tool name,
// so callers can produce a clean "unknown tool" error instead of a nil
// panic.
func Get(name string) (Tool, bool) {
	t, ok := Registry[name]
	return t, ok
}

package tooldef

import (
	"os"
	"path/filepath"
)

// candidatesRoot is ~/.sdkkeeper/candidates. Kept unexported; callers
// get it through Tool.CandidateRoot() so the path construction lives
// in exactly one place.
func candidatesRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".sdkkeeper", "candidates")
}

// TempRoot is ~/.sdkkeeper/tmp, scratch space for `install`'s
// download/extract cycle -- a sibling of candidates/, not system
// /tmp, since installer.Options.TempRoot must be on the same
// filesystem as the final destination for the atomic os.Rename to
// actually be atomic.
func TempRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".sdkkeeper", "tmp")
}

// CandidateRoot returns the sdkkeeper-managed root for this tool, e.g.
// ~/.sdkkeeper/candidates/java. Every version lives directly under
// here, either as a real directory (`install`) or a symlink (`add`,
// pointing at wherever the real install lives).
func (t Tool) CandidateRoot() string {
	return filepath.Join(candidatesRoot(), t.Name)
}

// DefaultPath returns the file path where this tool's remembered
// default version is stored, e.g. ~/.sdkkeeper/defaults/java -- a
// flat, one-line file containing just the version string. Set via
// `sk default java <version>`, applied via `sk use java default`.
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

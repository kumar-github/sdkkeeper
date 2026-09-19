package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"sdkkeeper/internal/term"
	"sdkkeeper/internal/tooldef"
)

func TestResolveUseJSON_UnknownToolIsAmbiguousTool(t *testing.T) {
	setTestHome(t, t.TempDir())
	_, jerr := resolveUseJSON("not-a-real-tool", "1.0")
	if jerr == nil {
		t.Fatal("expected an error for an unknown tool")
	}
	if jerr.Code != ErrCodeAmbiguousTool {
		t.Errorf("expected ambiguous_tool, got %q", jerr.Code)
	}
}

// TestResolveUseJSON_NoVersionIsVersionRequired confirms design doc
// §6: --format=json can never launch the picker, so omitting a
// version is unconditionally version_required, never a picker prompt.
func TestResolveUseJSON_NoVersionIsVersionRequired(t *testing.T) {
	setTestHome(t, t.TempDir())
	_, jerr := resolveUseJSON("java", "")
	if jerr == nil {
		t.Fatal("expected an error when no version is given")
	}
	if jerr.Code != ErrCodeVersionRequired {
		t.Errorf("expected version_required, got %q", jerr.Code)
	}
}

func TestResolveUseJSON_UnknownVersionIsNotFound(t *testing.T) {
	setTestHome(t, t.TempDir())
	_, jerr := resolveUseJSON("java", "99.0.0-temurin")
	if jerr == nil {
		t.Fatal("expected an error for a version that isn't installed")
	}
	if jerr.Code != ErrCodeNotFound {
		t.Errorf("expected not_found, got %q", jerr.Code)
	}
}

// TestResolveUseJSON_RealInstalledVersionActivates is the main happy
// path: a genuinely installed, multi-vendor version resolves cleanly,
// reports the correct vendor, and the "activated" action.
func TestResolveUseJSON_RealInstalledVersionActivates(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")

	data, jerr := resolveUseJSON("java", "21.0.2-temurin")
	if jerr != nil {
		t.Fatalf("expected success, got error: %+v", jerr)
	}
	if data.Tool != "java" || data.Version != "21.0.2-temurin" {
		t.Errorf("unexpected payload: %+v", data)
	}
	if data.Vendor == nil || *data.Vendor != "temurin" {
		t.Errorf("expected vendor=temurin, got %v", data.Vendor)
	}
	if data.Action != string(actionActivated) {
		t.Errorf("expected action=activated, got %q", data.Action)
	}
	if data.EnvVar != "JAVA_HOME" {
		t.Errorf("expected envVar=JAVA_HOME, got %q", data.EnvVar)
	}
	tool, _ := tooldef.Get("java")
	dir := filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+"21.0.2-temurin")
	if want := tool.HomePath(dir); data.EnvValue != want {
		t.Errorf("expected envValue=%q, got %q", want, data.EnvValue)
	}
	if want := tool.BinPath(dir); data.BinPath != want {
		t.Errorf("expected binPath=%q, got %q", want, data.BinPath)
	}
}

// TestResolveUseJSON_MatchesInteractiveResultExactly is the strongest
// guarantee usePayload's own EnvVar/EnvValue/BinPath fields can have:
// calling BOTH resolveUseJSON and the interactive resolveUse for the
// EXACT same installed version and confirming they report identical
// env var name/value and bin path -- not just "looks plausible", but
// genuinely, mechanically the same values the interactive shell
// wrapper's own eval would have used. If resolve.go's own
// Result-building logic (env var name, tool.HomePath, tool.BinPath)
// ever changes, this test breaks immediately if resolveUseJSON wasn't
// updated to match -- exactly the kind of cross-command consistency
// design doc §10 asks for, applied here to a pair that isn't two
// separate commands but the two separate CODE PATHS behind `use`
// itself.
func TestResolveUseJSON_MatchesInteractiveResultExactly(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "17.0.9-liberica")

	jsonData, jerr := resolveUseJSON("java", "17.0.9-liberica")
	if jerr != nil {
		t.Fatalf("resolveUseJSON failed: %+v", jerr)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	sess := &term.Session{Out: w}
	result, resolveErr := resolveUse(sess, term.Styles{}, "java", "17.0.9-liberica", "")
	w.Close()
	var discard bytes.Buffer
	discard.ReadFrom(r)
	if resolveErr != nil {
		t.Fatalf("resolveUse failed: %v", resolveErr)
	}

	interactiveValue, ok := result.EnvVars[jsonData.EnvVar]
	if !ok {
		t.Fatalf("interactive Result has no entry for %q at all: %+v", jsonData.EnvVar, result.EnvVars)
	}
	if interactiveValue != jsonData.EnvValue {
		t.Errorf("envValue mismatch: JSON says %q, interactive Result says %q", jsonData.EnvValue, interactiveValue)
	}

	if len(result.PathPrepends) != 1 {
		t.Fatalf("expected exactly one PathPrepend from the interactive path, got %d: %+v", len(result.PathPrepends), result.PathPrepends)
	}
	if result.PathPrepends[0].Dir != jsonData.BinPath {
		t.Errorf("binPath mismatch: JSON says %q, interactive Result says %q", jsonData.BinPath, result.PathPrepends[0].Dir)
	}
}

// TestResolveUseJSON_DefaultPlaceholderResolves confirms "default"
// (design doc's own "special value stays in the normal value's
// position" convention, same as the interactive path) is resolved to
// the stored default version before proceeding.
func TestResolveUseJSON_DefaultPlaceholderResolves(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, ok := tooldef.Get("java")
	if !ok {
		t.Fatal("tooldef.Get(java) failed")
	}
	installFakeJava(t, home, "17.0.9-temurin")
	if err := os.MkdirAll(filepath.Dir(tool.DefaultPath()), 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.WriteFile(tool.DefaultPath(), []byte("17.0.9-temurin"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	data, jerr := resolveUseJSON("java", "default")
	if jerr != nil {
		t.Fatalf("expected success, got error: %+v", jerr)
	}
	if data.Version != "17.0.9-temurin" {
		t.Errorf("expected the stored default version, got %q", data.Version)
	}
}

// TestResolveUseJSON_DefaultPlaceholderWithNoneSetIsNotFound confirms
// "default" with nothing ever set reports not_found, not a confusing
// version_required (there IS a version argument -- it's just that
// "default" doesn't resolve to anything).
func TestResolveUseJSON_DefaultPlaceholderWithNoneSetIsNotFound(t *testing.T) {
	setTestHome(t, t.TempDir())
	_, jerr := resolveUseJSON("java", "default")
	if jerr == nil {
		t.Fatal("expected an error")
	}
	if jerr.Code != ErrCodeNotFound {
		t.Errorf("expected not_found, got %q", jerr.Code)
	}
}

// TestResolveUseJSON_RequiresJavaWithoutJavaHomeIsVersionRequired
// confirms a RequiresJava tool (maven) can't silently resolve its own
// prerequisite via a picker under --format=json -- design doc §6's
// "never launches a picker" applies to the RequiresJava chain too, not
// just the top-level version argument.
func TestResolveUseJSON_RequiresJavaWithoutJavaHomeIsVersionRequired(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeMaven(t, home, "3.9.9")

	oldJavaHome, hadJavaHome := os.LookupEnv("JAVA_HOME")
	os.Unsetenv("JAVA_HOME")
	defer func() {
		if hadJavaHome {
			os.Setenv("JAVA_HOME", oldJavaHome)
		}
	}()

	_, jerr := resolveUseJSON("maven", "3.9.9")
	if jerr == nil {
		t.Fatal("expected an error when JAVA_HOME isn't set")
	}
	if jerr.Code != ErrCodeVersionRequired {
		t.Errorf("expected version_required, got %q", jerr.Code)
	}
}

// TestResolveUseJSON_RequiresJavaWithJavaHomeSetActivates confirms the
// happy path once the prerequisite is already satisfied -- and that a
// single-vendor tool (maven) always reports a nil vendor.
func TestResolveUseJSON_RequiresJavaWithJavaHomeSetActivates(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeMaven(t, home, "3.9.9")

	oldJavaHome, hadJavaHome := os.LookupEnv("JAVA_HOME")
	os.Setenv("JAVA_HOME", "/some/jdk") // content irrelevant -- only presence is checked
	defer func() {
		if hadJavaHome {
			os.Setenv("JAVA_HOME", oldJavaHome)
		} else {
			os.Unsetenv("JAVA_HOME")
		}
	}()

	data, jerr := resolveUseJSON("maven", "3.9.9")
	if jerr != nil {
		t.Fatalf("expected success, got error: %+v", jerr)
	}
	if data.Vendor != nil {
		t.Errorf("expected nil vendor for single-vendor maven, got %v", *data.Vendor)
	}
}

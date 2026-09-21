package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sdkkeeper/internal/tooldef"
)

func TestRunInitSkrc_NothingActiveWritesCommentOnlyFile(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	chdir(t, home)
	os.Unsetenv("JAVA_HOME")
	os.Unsetenv("MAVEN_HOME")
	os.Unsetenv("GRADLE_HOME")

	var runErr error
	out := withSession(t, func() {
		runErr = runInitSkrc()
	})
	if runErr != nil {
		t.Fatalf("expected success, got: %v", runErr)
	}
	if !strings.Contains(out, "no tools were active") {
		t.Errorf("expected an empty-file confirmation, got: %q", out)
	}

	content, err := os.ReadFile(filepath.Join(home, ".skrc"))
	if err != nil {
		t.Fatalf("expected .skrc to exist in cwd: %v", err)
	}
	if !strings.HasPrefix(string(content), "#") {
		t.Errorf("expected a comment-only placeholder, got: %q", content)
	}
	entries, err := parseSkrc(filepath.Join(home, ".skrc"))
	if err != nil {
		t.Fatalf("expected the placeholder to still parse cleanly: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

// TestRunInitSkrc_CreatesInCwdNotHome is the regression test for the
// exact bug report this design is built around: running 'sk init
// skrc' from a project directory must create the file THERE, not
// silently divert to $HOME even though $HOME is a different,
// unrelated directory.
func TestRunInitSkrc_CreatesInCwdNotHome(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	chdir(t, proj)

	var runErr error
	withSession(t, func() {
		runErr = runInitSkrc()
	})
	if runErr != nil {
		t.Fatalf("expected success, got: %v", runErr)
	}
	if _, err := os.Stat(filepath.Join(proj, ".skrc")); err != nil {
		t.Errorf("expected .skrc in the PROJECT directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".skrc")); !os.IsNotExist(err) {
		t.Error("expected $HOME/.skrc to NOT be created -- init only ever targets cwd")
	}
}

func TestRunInitSkrc_ActiveToolsAreSnapshotted(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	chdir(t, home)
	installFakeJava(t, home, "21.0.2-temurin")
	installFakeMaven(t, home, "3.9.9")

	tool, _ := tooldef.Get("java")
	javaDir := filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+"21.0.2-temurin")
	t.Setenv("JAVA_HOME", tool.HomePath(javaDir))

	mavenTool, _ := tooldef.Get("maven")
	mavenDir := filepath.Join(mavenTool.CandidateRoot(), mavenTool.FolderPrefix+"3.9.9")
	t.Setenv("MAVEN_HOME", mavenTool.HomePath(mavenDir))

	var runErr error
	withSession(t, func() {
		runErr = runInitSkrc()
	})
	if runErr != nil {
		t.Fatalf("expected success, got: %v", runErr)
	}

	entries, err := parseSkrc(filepath.Join(home, ".skrc"))
	if err != nil {
		t.Fatalf("could not parse written .skrc: %v", err)
	}
	got := map[string]string{}
	for _, e := range entries {
		got[e.Tool] = e.Version
	}
	if got["java"] != "21.0.2-temurin" {
		t.Errorf("expected java=21.0.2-temurin, got entries: %+v", entries)
	}
	if got["maven"] != "3.9.9" {
		t.Errorf("expected maven=3.9.9, got entries: %+v", entries)
	}
}

// TestRunInitSkrc_StaleEnvValueIsSilentlySkipped confirms the agreed
// behavior: an env var set to something inventory.Scan doesn't
// recognize is skipped, not reported as an error and not aborting the
// whole snapshot.
func TestRunInitSkrc_StaleEnvValueIsSilentlySkipped(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	chdir(t, home)
	// No real java installed at all -- JAVA_HOME points at nothing
	// inventory.Scan will ever find.
	t.Setenv("JAVA_HOME", filepath.Join(home, ".sdkkeeper", "candidates", "java", "JDK-does-not-exist"))

	var runErr error
	withSession(t, func() {
		runErr = runInitSkrc()
	})
	if runErr != nil {
		t.Fatalf("expected success, got: %v", runErr)
	}

	entries, err := parseSkrc(filepath.Join(home, ".skrc"))
	if err != nil {
		t.Fatalf("could not parse written .skrc: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected the stale JAVA_HOME to be skipped entirely, got entries: %+v", entries)
	}
}

func TestRunInitSkrc_RefusesWhenFileAlreadyExists(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	chdir(t, home)
	existing := filepath.Join(home, ".skrc")
	if err := os.WriteFile(existing, []byte("java=1.0\n"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	var runErr error
	out := withSession(t, func() {
		runErr = runInitSkrc()
	})
	if runErr == nil {
		t.Fatal("expected an error when cwd's .skrc already exists")
	}
	if !strings.Contains(out, "already exists") {
		t.Errorf("expected an already-exists message, got: %q", out)
	}

	// Confirm it genuinely wasn't touched/overwritten.
	content, err := os.ReadFile(existing)
	if err != nil {
		t.Fatalf("could not read existing file: %v", err)
	}
	if string(content) != "java=1.0\n" {
		t.Errorf("expected the existing file to be left untouched, got: %q", content)
	}
}

func TestRunRemoveSkrc_DeletesExistingFile(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	chdir(t, home)
	path := filepath.Join(home, ".skrc")
	if err := os.WriteFile(path, []byte("java=1.0\n"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	var runErr error
	out := withSession(t, func() {
		runErr = runRemoveSkrc()
	})
	if runErr != nil {
		t.Fatalf("expected success, got: %v", runErr)
	}
	if !strings.Contains(out, "Removed") {
		t.Errorf("expected a removal confirmation, got: %q", out)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected the file to be gone, stat error: %v", err)
	}
}

// TestRunRemoveSkrc_WalksUpFromNestedSubdirectory is remove's own
// mirror of init's cwd regression test: removal must target
// whichever .skrc is actually nearest to cwd (walking up, same as
// 'sk use'/'sk skrc' discovery), not a fixed $HOME location.
func TestRunRemoveSkrc_WalksUpFromNestedSubdirectory(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	proj := filepath.Join(home, "proj")
	nested := filepath.Join(proj, "src", "main")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	path := filepath.Join(proj, ".skrc")
	if err := os.WriteFile(path, []byte("java=1.0\n"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	chdir(t, nested)

	var runErr error
	out := withSession(t, func() {
		runErr = runRemoveSkrc()
	})
	if runErr != nil {
		t.Fatalf("expected success, got: %v", runErr)
	}
	if !strings.Contains(out, path) {
		t.Errorf("expected the confirmation to name the real project path %q, got: %q", path, out)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected the project's .skrc (found by walking up) to be removed")
	}
}

func TestRunRemoveSkrc_NothingFoundAnywhereIsNotAnError(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	chdir(t, home)

	var runErr error
	out := withSession(t, func() {
		runErr = runRemoveSkrc()
	})
	if runErr != nil {
		t.Fatalf("expected no error when there's nothing to remove, got: %v", runErr)
	}
	if !strings.Contains(out, "No .skrc found") {
		t.Errorf("expected a 'nothing to remove' message, got: %q", out)
	}
}

// TestInitCmd_SkrcArgDispatchesToRunInitSkrc confirms the cobra wiring:
// `sk init skrc` reaches runInitSkrc, and shell-name args (e.g.
// "zsh") are completely unaffected.
func TestInitCmd_SkrcArgDispatchesToRunInitSkrc(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	chdir(t, home)

	var runErr error
	withSession(t, func() {
		cmd := newInitCmd()
		runErr = cmd.RunE(cmd, []string{"skrc"})
	})
	if runErr != nil {
		t.Fatalf("expected success, got: %v", runErr)
	}
	if _, err := os.Stat(filepath.Join(home, ".skrc")); err != nil {
		t.Errorf("expected .skrc to have been created in cwd via 'sk init skrc': %v", err)
	}
}

func TestInitCmd_ShellArgStillPrintsShellIntegration(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	chdir(t, home)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	cmd := newInitCmd()
	err := cmd.RunE(cmd, []string{"zsh"})
	w.Close()
	os.Stdout = old
	if err != nil {
		t.Fatalf("expected success for 'sk init zsh', got: %v", err)
	}
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	out := string(buf[:n])
	if !strings.Contains(out, "sk") {
		t.Errorf("expected zsh shell integration output, got: %q", out)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".skrc")); !os.IsNotExist(statErr) {
		t.Error("expected 'sk init zsh' to never create a .skrc")
	}
}

// TestRemoveCmd_SkrcArgDispatchesToRunRemoveSkrc confirms the cobra
// wiring: `sk remove skrc` reaches runRemoveSkrc and is intercepted
// BEFORE requireTool would otherwise report "skrc" as an unknown
// tool.
func TestRemoveCmd_SkrcArgDispatchesToRunRemoveSkrc(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	chdir(t, home)
	path := filepath.Join(home, ".skrc")
	if err := os.WriteFile(path, []byte("java=1.0\n"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	var runErr error
	withSession(t, func() {
		cmd := newRemoveCmd()
		runErr = cmd.RunE(cmd, []string{"skrc"})
	})
	if runErr != nil {
		t.Fatalf("expected success, got: %v", runErr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected the nearest .skrc to have been removed via 'sk remove skrc'")
	}
}

func TestInitCmd_SkrcJSONCreatesRealFileAndReportsEntries(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	chdir(t, home)
	installFakeJava(t, home, "21.0.2-temurin")
	javaTool, _ := tooldef.Get("java")
	javaDir := filepath.Join(javaTool.CandidateRoot(), javaTool.FolderPrefix+"21.0.2-temurin")
	t.Setenv("JAVA_HOME", javaTool.HomePath(javaDir))

	oldFormat := outputFormat
	outputFormat = FormatJSON
	defer func() { outputFormat = oldFormat }()

	var runErr error
	out := captureStdout(t, func() {
		cmd := newInitCmd()
		runErr = cmd.RunE(cmd, []string{"skrc"})
	})
	if runErr != nil {
		t.Fatalf("expected success, got: %v", runErr)
	}
	if !strings.Contains(out, `"status":"ok"`) {
		t.Errorf("expected a success envelope, got: %q", out)
	}
	if !strings.Contains(out, `"tool":"java"`) {
		t.Errorf("expected java's entry in the payload, got: %q", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".skrc")); err != nil {
		t.Errorf("expected the JSON path to actually write the file too: %v", err)
	}
}

func TestInitCmd_SkrcJSONAlreadyExistsReturnsSkrcAlreadyExists(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	chdir(t, home)
	existing := filepath.Join(home, ".skrc")
	if err := os.WriteFile(existing, []byte("java=1.0\n"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	oldFormat := outputFormat
	outputFormat = FormatJSON
	defer func() { outputFormat = oldFormat }()

	var runErr error
	captureStdout(t, func() {
		cmd := newInitCmd()
		runErr = cmd.RunE(cmd, []string{"skrc"})
	})

	var cliErr *CLIError
	if !errors.As(runErr, &cliErr) {
		t.Fatalf("expected a *CLIError, got: %T (%v)", runErr, runErr)
	}
	if cliErr.Code != ErrCodeSkrcAlreadyExists {
		t.Errorf("expected skrc_already_exists, got %q", cliErr.Code)
	}
	if ExitCode(runErr) != 106 {
		t.Errorf("expected exit code 106, got %d", ExitCode(runErr))
	}
	content, err := os.ReadFile(existing)
	if err != nil || string(content) != "java=1.0\n" {
		t.Errorf("expected the existing file untouched, got %q (err: %v)", content, err)
	}
}

func TestRemoveCmd_SkrcJSONRemovesRealFile(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	chdir(t, home)
	path := filepath.Join(home, ".skrc")
	if err := os.WriteFile(path, []byte("java=1.0\n"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	oldFormat := outputFormat
	outputFormat = FormatJSON
	defer func() { outputFormat = oldFormat }()

	var runErr error
	out := captureStdout(t, func() {
		cmd := newRemoveCmd()
		runErr = cmd.RunE(cmd, []string{"skrc"})
	})
	if runErr != nil {
		t.Fatalf("expected success, got: %v", runErr)
	}
	if !strings.Contains(out, `"action":"removed"`) {
		t.Errorf("expected action:removed, got: %q", out)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected the JSON path to actually remove the file too")
	}
}

func TestRemoveCmd_SkrcJSONNothingFoundReportsNotFoundNotAnError(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	chdir(t, home)

	oldFormat := outputFormat
	outputFormat = FormatJSON
	defer func() { outputFormat = oldFormat }()

	var runErr error
	out := captureStdout(t, func() {
		cmd := newRemoveCmd()
		runErr = cmd.RunE(cmd, []string{"skrc"})
	})
	if runErr != nil {
		t.Fatalf("expected no error when nothing is found, got: %v", runErr)
	}
	if !strings.Contains(out, `"status":"ok"`) || !strings.Contains(out, `"action":"not_found"`) {
		t.Errorf("expected a success envelope with action:not_found, got: %q", out)
	}
	if !strings.Contains(out, `"path":null`) {
		t.Errorf("expected path:null, got: %q", out)
	}
}

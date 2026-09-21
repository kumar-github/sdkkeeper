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
		t.Fatalf("expected $HOME/.skrc to exist: %v", err)
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

func TestRunInitSkrc_ActiveToolsAreSnapshotted(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
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
	existing := filepath.Join(home, ".skrc")
	if err := os.WriteFile(existing, []byte("java=1.0\n"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	var runErr error
	out := withSession(t, func() {
		runErr = runInitSkrc()
	})
	if runErr == nil {
		t.Fatal("expected an error when $HOME/.skrc already exists")
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

func TestRunRemoveSkrc_MissingFileIsNotAnError(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	var runErr error
	out := withSession(t, func() {
		runErr = runRemoveSkrc()
	})
	if runErr != nil {
		t.Fatalf("expected no error when there's nothing to remove, got: %v", runErr)
	}
	if !strings.Contains(out, "does not exist") {
		t.Errorf("expected a 'nothing to remove' message, got: %q", out)
	}
}

// TestInitCmd_SkrcArgDispatchesToRunInitSkrc confirms the cobra wiring:
// `sk init skrc` reaches runInitSkrc, and shell-name args (e.g.
// "zsh") are completely unaffected.
func TestInitCmd_SkrcArgDispatchesToRunInitSkrc(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	var runErr error
	withSession(t, func() {
		cmd := newInitCmd()
		runErr = cmd.RunE(cmd, []string{"skrc"})
	})
	if runErr != nil {
		t.Fatalf("expected success, got: %v", runErr)
	}
	if _, err := os.Stat(filepath.Join(home, ".skrc")); err != nil {
		t.Errorf("expected $HOME/.skrc to have been created via 'sk init skrc': %v", err)
	}
}

func TestInitCmd_ShellArgStillPrintsShellIntegration(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

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
		t.Error("expected 'sk init zsh' to never touch $HOME/.skrc")
	}
}

// TestRemoveCmd_SkrcArgDispatchesToRunRemoveSkrc confirms the cobra
// wiring: `sk remove skrc` reaches runRemoveSkrc and is intercepted
// BEFORE requireTool would otherwise report "skrc" as an unknown
// tool.
func TestRemoveCmd_SkrcArgDispatchesToRunRemoveSkrc(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
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
		t.Error("expected $HOME/.skrc to have been removed via 'sk remove skrc'")
	}
}

func TestInitCmd_SkrcJSONCreatesRealFileAndReportsEntries(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
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

func TestRemoveCmd_SkrcJSONAlreadyGoneReportsNotPresentNotAnError(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	oldFormat := outputFormat
	outputFormat = FormatJSON
	defer func() { outputFormat = oldFormat }()

	var runErr error
	out := captureStdout(t, func() {
		cmd := newRemoveCmd()
		runErr = cmd.RunE(cmd, []string{"skrc"})
	})
	if runErr != nil {
		t.Fatalf("expected no error for an already-absent file, got: %v", runErr)
	}
	if !strings.Contains(out, `"status":"ok"`) || !strings.Contains(out, `"action":"not_present"`) {
		t.Errorf("expected a success envelope with action:not_present, got: %q", out)
	}
}

package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestReversedPaths(t *testing.T) {
	in := []PathPrepend{{Dir: "javaBin"}, {Dir: "mavenBin"}}
	got := reversedPaths(in)
	want := []string{"mavenBin", "javaBin"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("reversedPaths(%v) = %v, want %v", in, got, want)
	}
}

func TestWriteResult_MavenTakesPriorityOverJava(t *testing.T) {
	r := &Result{
		EnvVars: map[string]string{
			"JAVA_HOME":  "/java/home",
			"MAVEN_HOME": "/maven/home",
		},
		// built the way resolveUse actually builds it: prerequisite
		// (java) first, dependent tool (maven) appended last
		PathPrepends: []PathPrepend{
			{Dir: "/java/home/bin", StripPrefix: "/java/candidates"},
			{Dir: "/maven/home/bin", StripPrefix: "/maven/candidates"},
		},
	}

	out := captureStdout(t, func() { writeResult(r, ShellZsh) })

	pathLine := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "export PATH=") {
			pathLine = line
		}
	}
	if pathLine == "" {
		t.Fatalf("no export PATH line found in output:\n%s", out)
	}

	mavenIdx := strings.Index(pathLine, "/maven/home/bin")
	javaIdx := strings.Index(pathLine, "/java/home/bin")
	if mavenIdx == -1 || javaIdx == -1 {
		t.Fatalf("expected both bin dirs in PATH line: %s", pathLine)
	}
	if mavenIdx > javaIdx {
		t.Errorf("expected maven's bin to appear BEFORE java's bin in PATH (maven should take priority as the more specific tool), got: %s", pathLine)
	}
}

func TestWriteResult_JSONMode(t *testing.T) {
	r := &Result{
		EnvVars:      map[string]string{"JAVA_HOME": "/java/home"},
		PathPrepends: []PathPrepend{{Dir: "/java/home/bin", StripPrefix: "/java/candidates"}},
	}

	out := captureStdout(t, func() { writeResult(r, ShellJSON) })

	if !strings.Contains(out, `"JAVA_HOME":"/java/home"`) {
		t.Errorf("expected JAVA_HOME in JSON output, got: %s", out)
	}
	if !strings.Contains(out, `"PATH_PREPEND":"/java/home/bin"`) {
		t.Errorf("expected PATH_PREPEND in JSON output, got: %s", out)
	}
}

func TestWriteResult_NilResultProducesNoOutput(t *testing.T) {
	out := captureStdout(t, func() { writeResult(nil, ShellZsh) })
	if out != "" {
		t.Errorf("expected no output for nil result, got: %q", out)
	}
}

func TestStripPrefixes_DeduplicatesPreservingOrder(t *testing.T) {
	prepends := []PathPrepend{
		{Dir: "/maven/bin", StripPrefix: "/candidates/maven"},
		{Dir: "/java/bin", StripPrefix: "/candidates/java"},
		{Dir: "/java/bin-again", StripPrefix: "/candidates/java"}, // duplicate prefix
	}
	got := stripPrefixes(prepends)
	want := []string{"/candidates/maven", "/candidates/java"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("expected order %v, got %v", want, got)
			break
		}
	}
}

func TestStripPrefixes_IgnoresEmptyStripPrefix(t *testing.T) {
	prepends := []PathPrepend{{Dir: "/some/bin", StripPrefix: ""}}
	if got := stripPrefixes(prepends); len(got) != 0 {
		t.Errorf("expected no prefixes for an empty StripPrefix, got: %v", got)
	}
}

// TestPathAssignmentScript_NoStripPrefixesIsPlainPrepend confirms the
// simple, unconditional case (nothing on PATH yet for any tool being
// activated) stays a plain export, not the more complex strip loop.
func TestPathAssignmentScript_NoStripPrefixesIsPlainPrepend(t *testing.T) {
	got := zshPathAssignmentScript([]string{"/java/bin"}, nil)
	want := `export PATH="/java/bin:$PATH"`
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// TestPathAssignmentScript_StripLoopIsRealAndCorrect is a regression
// test for the exact reported bug: `sk use` only ever PREPENDED,
// never removing an earlier prepend for the same tool -- switching
// JDKs (or the same activation firing more than once) silently
// accumulated multiple entries on PATH without bound, so removing the
// active one still left `java` resolving to an EARLIER, still-
// accumulated entry. Confirms the actual fix is present: zsh's own
// array-splitting syntax (not shell parameter expansion, confirmed
// broken for path-shaped input in an earlier round; not external
// tools, confirmed unsafe in that same round), and the strip pattern
// correctly includes the given prefix.
func TestPathAssignmentScript_StripLoopIsRealAndCorrect(t *testing.T) {
	got := zshPathAssignmentScript([]string{"/java/new/bin"}, []string{"/candidates/java"})
	if !strings.Contains(got, "${(s.:.)PATH}") {
		t.Errorf("expected zsh's native array-splitting syntax, got: %q", got)
	}
	if strings.Contains(got, "${PATH//") {
		t.Errorf("expected NO shell parameter expansion syntax (confirmed broken for path-shaped input), got: %q", got)
	}
	if strings.Contains(got, "tr ") || strings.Contains(got, "grep ") || strings.Contains(got, "sed ") {
		t.Errorf("expected NO external tool dependency (confirmed unsafe), got: %q", got)
	}
	if !strings.Contains(got, "'/candidates/java'/*") {
		t.Errorf("expected the strip prefix pattern to appear correctly quoted, got: %q", got)
	}
	if !strings.Contains(got, "/java/new/bin:$_sk_new_path") {
		t.Errorf("expected the new dir prepended onto the filtered path, got: %q", got)
	}
}

// TestPathAssignmentScript_MultiplePrefixesORdTogether confirms the
// prerequisite-chain case (e.g. Maven requiring Java) strips BOTH
// tools' earlier entries in one pass, not just the first.
func TestPathAssignmentScript_MultiplePrefixesORdTogether(t *testing.T) {
	got := zshPathAssignmentScript(
		[]string{"/maven/new/bin", "/java/new/bin"},
		[]string{"/candidates/java", "/candidates/maven"},
	)
	if !strings.Contains(got, "'/candidates/java'/*|'/candidates/maven'/*") {
		t.Errorf("expected both prefixes OR'd together in one case pattern, got: %q", got)
	}
}

func TestWriteDeactivate_PlainMode_EnvVarOnly(t *testing.T) {
	out := captureStdout(t, func() { writeDeactivate("JAVA_HOME", "", ShellZsh) })
	if strings.TrimSpace(out) != "unset JAVA_HOME" {
		t.Errorf("expected 'unset JAVA_HOME', got: %q", out)
	}
}

func TestWriteDeactivate_JSONMode_EnvVarOnly(t *testing.T) {
	out := captureStdout(t, func() { writeDeactivate("JAVA_HOME", "", ShellJSON) })
	if !strings.Contains(out, `"unset":["JAVA_HOME"]`) {
		t.Errorf("expected an 'unset' array containing JAVA_HOME in JSON output, got: %q", out)
	}
	if strings.Contains(out, "path_remove") {
		t.Errorf("expected NO path_remove key when binPath is empty, got: %q", out)
	}
}

// TestWriteDeactivate_PlainMode_IncludesPathStrip is a regression test
// for THREE real bugs caught via actual use, in sequence, each one
// found only by actually testing end-to-end in a real zsh process:
//
//  1. Clearing the env var alone left `java -version` (PATH-resolved)
//     still reporting a different, unexpected JDK, since PATH still
//     had the removed version's bin directory prepended (and `sk use`
//     never removes an EARLIER prepend when switching versions within
//     one shell session).
//  2. The first fix (${PATH//search/replace} shell parameter
//     expansion) was itself broken: that syntax uses "/" as its own
//     delimiter between search and replace, fundamentally
//     incompatible with a file path AS the search text. Silently
//     produced catastrophically garbled PATH output, not even an
//     error.
//  3. The tr/grep -Fx/sed pipeline that replaced THAT was ALSO
//     broken: those are external tools, which must themselves be
//     findable on the CURRENT PATH to run at all -- but modifying
//     PATH is the whole point. When PATH consisted only of sk-managed
//     bin directories (confirmed directly, not an exotic edge case),
//     the pipeline couldn't even find tr/grep/sed, produced empty
//     output, and PATH got wiped out ENTIRELY.
//
// The final fix uses only zsh built-ins (a native array-splitting
// loop, no external process spawned at all), which cannot fail either
// way regardless of what PATH contains when it runs.
func TestWriteDeactivate_PlainMode_IncludesPathStrip(t *testing.T) {
	out := captureStdout(t, func() { writeDeactivate("JAVA_HOME", "/opt/jdk/bin", ShellZsh) })
	if !strings.Contains(out, "unset JAVA_HOME") {
		t.Errorf("expected the env var to still be unset, got: %q", out)
	}
	if strings.Contains(out, "${PATH//") {
		t.Errorf("expected NO shell parameter expansion syntax (confirmed broken for path-shaped input), got: %q", out)
	}
	if strings.Contains(out, "tr ") || strings.Contains(out, "grep ") || strings.Contains(out, "sed ") {
		t.Errorf("expected NO external tool dependency (confirmed unsafe -- those tools may not be findable on the current PATH), got: %q", out)
	}
	if !strings.Contains(out, "${(s.:.)PATH}") {
		t.Errorf("expected zsh's own native array-splitting syntax, got: %q", out)
	}
	if !strings.Contains(out, "'/opt/jdk/bin'") {
		t.Errorf("expected the shell-quoted bin dir to appear in the comparison, got: %q", out)
	}
}

func TestWriteDeactivate_JSONMode_IncludesPathRemove(t *testing.T) {
	out := captureStdout(t, func() { writeDeactivate("JAVA_HOME", "/opt/jdk/bin", ShellJSON) })
	if !strings.Contains(out, `"unset":["JAVA_HOME"]`) {
		t.Errorf("expected JAVA_HOME in the unset array, got: %q", out)
	}
	if !strings.Contains(out, `"path_remove":["/opt/jdk/bin"]`) {
		t.Errorf("expected the bin dir in a path_remove array, got: %q", out)
	}
}

// TestWriteDeactivate_EmptyArgsProduceNoOutput confirms a genuinely
// empty envVar AND binPath (shouldn't normally happen) doesn't emit
// meaningless, empty commands.
func TestWriteDeactivate_EmptyArgsProduceNoOutput(t *testing.T) {
	out := captureStdout(t, func() { writeDeactivate("", "", ShellZsh) })
	if out != "" {
		t.Errorf("expected no output when both args are empty, got: %q", out)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String()
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/opt/jdk/bin":        `'/opt/jdk/bin'`,
		"":                    `''`,
		"path/with'quote/bin": `'path/with'\''quote/bin'`,
	}
	for in, want := range cases {
		got := shellQuote(in)
		if got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPowershellQuote(t *testing.T) {
	cases := map[string]string{
		"C:\\Users\\me\\jdk":  `'C:\Users\me\jdk'`,
		"":                    `''`,
		"path/with'quote/bin": `'path/with''quote/bin'`,
	}
	for in, want := range cases {
		got := powershellQuote(in)
		if got != want {
			t.Errorf("powershellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPowershellPathAssignmentScript_NoStripPrefixesIsPlainPrepend
// mirrors TestPathAssignmentScript_NoStripPrefixesIsPlainPrepend
// (the zsh equivalent) -- same simple, unconditional case.
func TestPowershellPathAssignmentScript_NoStripPrefixesIsPlainPrepend(t *testing.T) {
	got := powershellPathAssignmentScript([]string{"C:\\java\\bin"}, nil)
	want := `$env:Path = 'C:\java\bin' + ';' + $env:Path`
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// TestPowershellPathAssignmentScript_UsesSemicolonNotColon confirms
// the single most important, genuinely PLATFORM-SPECIFIC difference
// from the zsh formatter: Windows' PATH is semicolon-delimited, not
// colon-delimited. Multiple newDirs joined with the wrong separator
// would silently produce a single, malformed PATH entry rather than
// multiple real ones.
func TestPowershellPathAssignmentScript_UsesSemicolonNotColon(t *testing.T) {
	got := powershellPathAssignmentScript([]string{"C:\\a\\bin", "C:\\b\\bin"}, nil)
	if !strings.Contains(got, `'C:\a\bin;C:\b\bin'`) {
		t.Errorf("expected newDirs joined with ';', got: %q", got)
	}
	if strings.Contains(got, `C:\a\bin:C:\b\bin`) {
		t.Errorf("expected NO ':' joining newDirs (that's the zsh/POSIX separator, wrong on Windows), got: %q", got)
	}
}

// TestPowershellPathAssignmentScript_StripLogicIsGenuinePowerShell is
// a regression test confirming the strip/prepend logic uses REAL
// PowerShell syntax (Where-Object, -split/-join, -like), not a
// transliteration of the zsh case-pattern script (which wouldn't
// parse as PowerShell at all) -- and that neither zsh's own syntax
// nor a POSIX ':' PATH separator leaked into it.
func TestPowershellPathAssignmentScript_StripLogicIsGenuinePowerShell(t *testing.T) {
	got := powershellPathAssignmentScript([]string{"C:\\java\\new\\bin"}, []string{"C:\\candidates\\java"})
	if !strings.Contains(got, "Where-Object") {
		t.Errorf("expected genuine PowerShell Where-Object filtering, got: %q", got)
	}
	if !strings.Contains(got, "-split ';'") || !strings.Contains(got, "-join ';'") {
		t.Errorf("expected PATH split/join on ';' (Windows' real separator), got: %q", got)
	}
	if strings.Contains(got, "${(s.:.)PATH}") || strings.Contains(got, "case \"$") {
		t.Errorf("expected NO zsh-specific syntax to have leaked in, got: %q", got)
	}
	if !strings.Contains(got, `'C:\candidates\java' + '\*'`) {
		t.Errorf("expected the strip prefix correctly quoted and concatenated with a PowerShell wildcard, got: %q", got)
	}
}

// TestPowershellFormatter_FormatResult_UsesEnvColonSyntax confirms
// the env-var assignment line uses PowerShell's REAL syntax
// ($env:VAR = "value"), not zsh's `export VAR=value` -- the most
// visible, easy-to-get-wrong platform difference of all.
func TestPowershellFormatter_FormatResult_UsesEnvColonSyntax(t *testing.T) {
	got := powershellFormatter{}.formatResult(map[string]string{"JAVA_HOME": "C:\\jdk"}, []string{"C:\\jdk\\bin"}, nil)
	if !strings.Contains(got, "$env:JAVA_HOME = 'C:\\jdk'") {
		t.Errorf("expected PowerShell $env: syntax, got: %q", got)
	}
	if strings.Contains(got, "export ") {
		t.Errorf("expected NO zsh 'export' syntax to have leaked in, got: %q", got)
	}
}

// TestPowershellFormatter_FormatDeactivate_UsesRemoveItem confirms
// env-var clearing uses PowerShell's REAL mechanism (Remove-Item on
// the Env: drive) -- PowerShell has no "unset" keyword at all, unlike
// zsh, so this is a genuinely different OPERATION, not just different
// syntax for the same one.
func TestPowershellFormatter_FormatDeactivate_UsesRemoveItem(t *testing.T) {
	got := powershellFormatter{}.formatDeactivate("JAVA_HOME", "C:\\jdk\\bin")
	if !strings.Contains(got, "Remove-Item Env:JAVA_HOME") {
		t.Errorf("expected Remove-Item on the Env: drive, got: %q", got)
	}
	if strings.Contains(got, "unset ") {
		t.Errorf("expected NO zsh 'unset' syntax to have leaked in, got: %q", got)
	}
}

// TestFormatterFor_SelectsCorrectFormatter confirms the dispatch
// itself picks the right concrete formatter for each ShellFormat
// value, including the fallback default for an unrecognized value
// (mirroring parseShellFormat's own fail-open reasoning in root.go).
func TestFormatterFor_SelectsCorrectFormatter(t *testing.T) {
	if _, ok := formatterFor(ShellZsh).(zshFormatter); !ok {
		t.Error("expected ShellZsh to select zshFormatter")
	}
	if _, ok := formatterFor(ShellJSON).(jsonFormatter); !ok {
		t.Error("expected ShellJSON to select jsonFormatter")
	}
	if _, ok := formatterFor(ShellPowerShell).(powershellFormatter); !ok {
		t.Error("expected ShellPowerShell to select powershellFormatter")
	}
	if _, ok := formatterFor(ShellFormat(999)).(zshFormatter); !ok {
		t.Error("expected an unrecognized ShellFormat value to fall back to zshFormatter")
	}
}

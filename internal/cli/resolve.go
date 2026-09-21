package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"sdkkeeper/internal/inventory"
	"sdkkeeper/internal/picker"
	"sdkkeeper/internal/term"
	"sdkkeeper/internal/tooldef"
)

// ErrCancelled and ErrNotFound are the two "nothing to do" cases: the
// user backed out of the picker, or asked for a version that isn't
// installed. Both are genuine failures (non-zero exit), so a caller
// scripting against sk (e.g. `sk use java 21 && mvn install`) can rely
// on that. The user-facing message is already printed at the point of
// failure inside resolveUse -- callers should not print err.Error()
// again (see use.go).
var (
	ErrCancelled = errors.New("no version selected")
	ErrNotFound  = errors.New("version not found")
)

// PathPrepend is one directory to prepend to PATH, paired with the
// prefix identifying any earlier, sk-managed PATH entry for the same
// tool that must be removed first -- without this, switching versions
// (or re-activating) accumulates unbounded duplicate entries, and
// removing the active version can leave PATH resolving to a stale one.
type PathPrepend struct {
	Dir         string
	StripPrefix string
}

// Result is everything a `use` resolution hands back to the shell
// wrapper. A map/slice, not a single string pair, since a tool with
// RequiresJava may need to emit both its own env var and JAVA_HOME
// together in one combined response.

type Result struct {
	// EnvVars is every environment variable to set, e.g.
	// {"JAVA_HOME": "...", "MAVEN_HOME": "..."}.
	EnvVars map[string]string

	// PathPrepends is every directory to prepend to PATH, in order --
	// the LAST entry ends up FIRST in the final PATH, so a dependent
	// tool's bin directory takes priority over its prerequisite's
	// (e.g. Maven ahead of Java).
	PathPrepends []PathPrepend

	// Messages holds confirmation text not yet shown to the user --
	// either folded into the next picker's banner, or printed directly
	// once resolution completes (see resolveUse's design note). Never
	// printed immediately at generation time; that caused a picker
	// flash/cutoff bug.
	Messages []string

	// ActivatedTool and ActivatedVersion record what resolveUse
	// actually activated, set only on success (e.g. after a picker
	// resolves to a real choice) -- empty otherwise. Not used by
	// writeResult; only by callers that need to know the resolved
	// version after the fact, e.g. use.go's .skrc override check,
	// which must compare against the real outcome, not the (possibly
	// empty, picker-triggering) argument the user typed.
	ActivatedTool    string
	ActivatedVersion string
}

func newResult() *Result {
	return &Result{EnvVars: map[string]string{}}
}

// readDefault reads tool's stored default, returning ("", nil) if
// none was ever set, distinct from a genuine read error.
func readDefault(tool tooldef.Tool) (string, error) {
	data, err := os.ReadFile(tool.DefaultPath())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// merge is nil-safe on other: a failed prerequisite call may still
// return a partially-populated Result alongside its error.
func (r *Result) merge(other *Result) {
	if other == nil {
		return
	}
	for k, v := range other.EnvVars {
		r.EnvVars[k] = v
	}
	r.PathPrepends = append(r.PathPrepends, other.PathPrepends...)
	r.Messages = append(r.Messages, other.Messages...)
}

// isEmpty reports whether r has nothing worth applying to the shell.
// Deliberately ignores Messages -- pending confirmation text with no
// env/PATH change isn't something writeResult needs to treat specially.
func (r *Result) isEmpty() bool {
	return r == nil || (len(r.EnvVars) == 0 && len(r.PathPrepends) == 0)
}

// joinBanner concatenates two banner strings with a blank line
// between them, omitting either side if empty.
func joinBanner(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "\n\n" + b
}

// showPendingBanner prints banner to sess.Out with a leading and
// trailing blank line, if non-empty (no-op otherwise). Used when a
// prerequisite's confirmation has nothing further to fold into,
// because the current resolution won't show its own picker. Returns
// true if it printed anything -- callers use this to decide whether
// they still need their own leading blank line before whatever
// they print next.
func showPendingBanner(sess *term.Session, banner string) bool {
	if banner == "" {
		return false
	}
	fmt.Fprintln(sess.Out)
	fmt.Fprintln(sess.Out, banner)
	fmt.Fprintln(sess.Out)
	return true
}

// flushMessages prints any pending result.Messages to sess.Out (each
// with a leading blank line), then clears them. Ensures an earlier
// event's confirmation (e.g. a prerequisite JDK selection) always
// prints before a later event's message, and isn't printed again by
// use.go's final flush. Distinct from showPendingBanner: by the time
// a picker has run and been cancelled, pendingBanner is already
// consumed, but result.Messages was deliberately preserved through
// that (see the RequiresJava block below).
func flushMessages(sess *term.Session, result *Result) bool {
	if len(result.Messages) == 0 {
		return false
	}
	for _, m := range result.Messages {
		fmt.Fprintln(sess.Out)
		fmt.Fprintln(sess.Out, m)
	}
	result.Messages = nil
	return true
}

// resolveUse is the core logic shared by every tool's `use` command:
// interactive picker or direct version argument, the RequiresJava
// prerequisite chain, and not-found handling. use.go is a thin
// wrapper: parse flags, call this, print the result.
//
// incomingBanner is context from a parent call in the RequiresJava
// chain (e.g. "No JDK selected — choose one first:"), shown as this
// call's own picker banner if one ends up being shown. Pass "" at the
// top level.
//
// The banner/Messages mechanism avoids a UX bug where printing a
// status line then immediately launching an alt-screen picker wipes
// that line out a moment later. Pending text is either folded into
// the next picker's banner, or printed directly once resolution is
// fully done -- never printed immediately at generation time.
//
// Every failure path below returns `result`, not nil, even on error,
// so a real prior success (e.g. a JDK genuinely selected) isn't
// discarded just because a later step in the chain failed.
func resolveUse(sess *term.Session, styles term.Styles, toolName, versionArg, incomingBanner string) (*Result, error) {
	tool, ok := tooldef.Get(toolName)
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", toolName)
	}

	// "default" occupies the same argument slot a real version would
	// (matching `docker run image:latest`'s convention), resolved
	// here once before anything else runs.
	if versionArg == "default" {
		stored, err := readDefault(tool)
		if err != nil {
			return nil, err
		}
		if stored == "" {
			fmt.Fprintln(sess.Out)
			fmt.Fprintln(sess.Out, styles.Error.Render(
				fmt.Sprintf("No default %s set. Run `sk default %s <version>` to set one.", tool.DisplayName, tool.Name),
			))
			return newResult(), ErrNotFound
		}
		versionArg = stored
	}

	result := newResult()
	pendingBanner := incomingBanner
	// Tracks whether anything has been printed yet, since
	// showPendingBanner/flushMessages are no-ops with nothing pending.
	printedSomething := false

	// Checked before the RequiresJava prerequisite chain: there's
	// nothing to activate for Maven if Maven itself has nothing
	// installed, regardless of Java's state, so this reports the
	// correct tool instead of a bare Java-prerequisite message.
	version := versionArg
	var versions []inventory.Version
	if version == "" {
		var err error
		versions, err = inventory.Scan(tool)
		if err != nil {
			return result, err
		}
		if len(versions) == 0 {
			if showPendingBanner(sess, pendingBanner) {
				printedSomething = true
			}
			result.Messages = nil // shown above via pendingBanner; don't repeat at the end
			if !printedSomething {
				fmt.Fprintln(sess.Out)
			}
			fmt.Fprintln(sess.Out, styles.Neutral.Render(fmt.Sprintf("No %s versions found to use.", tool.DisplayName)))
			return result, ErrNotFound
		}
	} else {
		// Same principle, extended to an explicit version: validated
		// to exist before touching the java prerequisite chain, since
		// there's nothing to activate regardless of java's state.
		if _, found := inventory.Find(tool, version); !found {
			if showPendingBanner(sess, pendingBanner) {
				printedSomething = true
			}
			result.Messages = nil
			if !printedSomething {
				fmt.Fprintln(sess.Out)
			}
			allVersions, _ := inventory.Scan(tool)
			msg := inventory.FormatNotFound(tool.FolderPrefix, version, "use", tool.DisplayName, allVersions)
			fmt.Fprintln(sess.Out, styles.Error.Render(msg))
			return result, ErrNotFound
		}
	}

	if tool.RequiresJava {
		if _, alreadySet := os.LookupEnv("JAVA_HOME"); !alreadySet {
			prompt := styles.Error.Render("No JDK selected — choose one first:")
			javaResult, err := resolveUse(sess, styles, "java", "", prompt)
			result.merge(javaResult)
			if len(result.Messages) > 0 {
				// Messages is deliberately NOT cleared here -- kept so
				// this confirmation is also printed persistently at
				// the end (use.go's final flush), not just shown
				// transiently as the next picker's banner.
				pendingBanner = joinBanner(pendingBanner, strings.Join(result.Messages, "\n"))
			}
			if err != nil {
				return result, err
			}
		}
	}

	if version == "" {
		if !sess.HasTTY {
			// Mirrors the cancellation case below: a prior successful
			// step must still reach the scrollback. Prints its own
			// message directly, like every other error case here.
			flushMessages(sess, result)
			err := versionRequiredNoTTY(tool, "use")
			fmt.Fprintln(sess.Out)
			fmt.Fprintln(sess.Out, styles.Error.Render(err.Error()))
			return result, err
		}
		title := fmt.Sprintf("Select %s version", tool.DisplayName)
		chosen, err := picker.RunGrouped(sess, title, buildPickerGroups(tool, versions), pendingBanner)
		pendingBanner = "" // consumed as this picker's banner regardless of outcome below
		if err != nil {
			return result, err
		}
		if chosen == "" {
			flushMessages(sess, result)
			fmt.Fprintln(sess.Out)
			fmt.Fprintln(sess.Out, styles.Neutral.Render(fmt.Sprintf("No %s version selected to use.", tool.DisplayName)))
			return result, ErrCancelled
		}
		version = chosen
	} else {
		// Direct version arg given -- no picker for this tool, so any
		// pending banner has nowhere to fold into; show it now.
		if showPendingBanner(sess, pendingBanner) {
			printedSomething = true
		}
		pendingBanner = ""
		// A real, permanent console print, not a transient banner --
		// clear Messages so use.go's final flush doesn't repeat it.
		result.Messages = nil
	}

	v, ok := inventory.Find(tool, version)
	if !ok {
		if flushMessages(sess, result) {
			printedSomething = true
		}
		if !printedSomething {
			fmt.Fprintln(sess.Out)
		}
		versions, _ := inventory.Scan(tool)
		msg := inventory.FormatNotFound(tool.FolderPrefix, version, "use", tool.DisplayName, versions)
		fmt.Fprintln(sess.Out, styles.Error.Render(msg))
		return result, ErrNotFound
	}

	if tool.EnvVar != "" {
		result.EnvVars[tool.EnvVar] = tool.HomePath(v.Path)
	}
	result.PathPrepends = append(result.PathPrepends, PathPrepend{
		Dir:         tool.BinPath(v.Path),
		StripPrefix: tool.CandidateRoot(),
	})

	result.ActivatedTool = tool.Name
	result.ActivatedVersion = version

	confirmation := styles.Success.Render(
		fmt.Sprintf("\u2713 %s %s — active for this shell session only", tool.DisplayName, version),
	)
	// Subordinate detail line, not the primary confirmation.
	if tool.EnvVar != "" {
		confirmation += "\n" + styles.Detail.Render(
			fmt.Sprintf("  \u2514\u2500 %s=%s", tool.EnvVar, tool.HomePath(v.Path)),
		)
	}
	result.Messages = append(result.Messages, confirmation)

	return result, nil
}

// clearActive implements `sk use <tool> null`: deactivates whatever
// is currently active for tool in this shell, without touching disk.
// Short-circuits entirely rather than flowing through resolveUse --
// there's nothing to pick or chain a prerequisite for when clearing.
func clearActive(sess *term.Session, styles term.Styles, toolName string, format ShellFormat) error {
	tool, err := requireTool(toolName)
	if err != nil {
		return err
	}

	fmt.Fprintln(sess.Out)

	if tool.EnvVar == "" {
		fmt.Fprintln(sess.Out, styles.Neutral.Render(fmt.Sprintf("%s has no activation state to clear.", tool.DisplayName)))
		return nil
	}

	current, isSet := os.LookupEnv(tool.EnvVar)
	if !isSet || current == "" {
		fmt.Fprintln(sess.Out, styles.Neutral.Render(fmt.Sprintf("No %s currently active in this shell — nothing to clear.", tool.DisplayName)))
		return nil
	}

	versions, err := inventory.Scan(tool)
	if err != nil {
		fmt.Fprintln(sess.Out, styles.Error.Render(fmt.Sprintf("\u2717 Could not check installed versions: %s", err)))
		return err
	}

	// Stays empty if current doesn't match anything sk recognizes --
	// the env var is still cleared, but there's no known PATH entry
	// to safely strip.
	var binPath string
	if v, ok := findActiveVersion(tool, versions, current); ok {
		binPath = tool.BinPath(v.Path)
	}

	writeDeactivate(tool.EnvVar, binPath, format)
	fmt.Fprintln(sess.Out, styles.Success.Render(
		fmt.Sprintf("\u2713 %s cleared for this shell — no %s currently active", tool.EnvVar, tool.DisplayName),
	))
	return nil
}

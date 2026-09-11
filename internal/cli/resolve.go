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

// ErrCancelled and ErrNotFound are returned for the two "nothing to do"
// paths -- the user backed out of the picker, or asked for a version
// that isn't installed. Both are genuine failures for exit-code
// purposes (matching the original shell-script version's `return 1` in
// both cases) -- a caller scripting against sk (e.g. `sk use java 21 &&
// mvn install`) must be able to rely on a non-zero exit code here, or
// the whole fail-fast design is silently defeated for anyone chaining
// commands. The user-facing message for both is already printed to
// sess.Out at the point of failure, inside resolveUse -- callers should
// NOT print err.Error() again on top of it (see use.go).
var (
	ErrCancelled = errors.New("no version selected")
	ErrNotFound  = errors.New("version not found")
)

// Result is everything a `use` resolution needs to hand back to the
// shell wrapper. Deliberately a map/slice, not a single string pair --
// resolving a tool with RequiresJava may need to emit BOTH that tool's
// own env var (e.g. MAVEN_HOME) AND JAVA_HOME together, in one combined
// response, since a single sk invocation only gets evaluated once by
// the wrapper.
// PathPrepend is one directory to prepend to PATH, paired with the
// "strip prefix" identifying any EARLIER, sk-managed entry for the
// SAME tool that must be removed first.
//
// A real, confirmed bug this exists to fix: `sk use` always PREPENDED
// without ever cleaning up an earlier prepend for the same tool --
// switching JDKs (or re-running the same activation, e.g. via the
// documented "OPTIONAL auto-activation" shell snippet firing more
// than once in a session) silently accumulated MULTIPLE entries for
// the same tool on PATH, without bound. The direct, observed
// consequence: removing the currently-active JDK correctly cleared
// JAVA_HOME and stripped THAT one specific entry, but `java` still
// resolved to whichever EARLIER, still-accumulated entry came next --
// looking exactly like "removing the active JDK doesn't work", even
// though the removed entry itself genuinely was gone. StripPrefix
// (tool.CandidateRoot(), e.g. "~/.sdkkeeper/candidates/java") ensures
// `use` never lets more than ONE entry per tool exist on PATH at a
// time, regardless of how many times a tool's version is switched or
// re-activated within one session.
type PathPrepend struct {
	Dir         string
	StripPrefix string
}

type Result struct {
	// EnvVars is every environment variable to set, e.g.
	// {"JAVA_HOME": "...", "MAVEN_HOME": "..."}.
	EnvVars map[string]string

	// PathPrepends is every directory to prepend to PATH, in the order
	// they should be prepended -- the LAST entry ends up FIRST in the
	// final PATH, matching how sequential shell `export PATH=...`
	// statements would behave. This lets a dependent tool's own bin
	// directory take priority over its prerequisite's (e.g. Maven's
	// bin ahead of Java's), matching the original shell scripts'
	// behavior.
	PathPrepends []PathPrepend

	// Messages holds confirmation text (e.g. "✓ JDK 21.0.2 — active for
	// this shell session only") that hasn't been shown to the user yet.
	// A message here is either later folded into the banner of the NEXT
	// picker shown in the same resolution chain (see resolveUse), or --
	// if nothing further happens -- printed directly by the caller
	// (use.go) once resolution fully completes. Messages are NEVER
	// printed immediately at the point they're generated; that
	// immediate-print approach is what originally caused the flash/
	// cutoff bug (see the design note on resolveUse below).
	Messages []string
}

func newResult() *Result {
	return &Result{EnvVars: map[string]string{}}
}

// readDefault reads the stored default version for tool, returning
// ("", nil) if none has ever been set (the file simply doesn't exist)
// -- distinct from a genuine read error (permissions, etc.), which
// callers should surface rather than silently treat the same as "no
// default set".
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

// merge is nil-safe on other: a prerequisite call that itself failed
// partway through (see resolveUse) may still return a non-nil, partially
// populated Result alongside its error -- but being defensive here
// costs nothing and protects against any future oversight in a deeper
// dependency chain.
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

// isEmpty reports whether r has nothing worth applying to the shell --
// used to treat a genuinely-empty Result the same as a nil one (see
// writeResult). Deliberately does NOT consider Messages -- a Result
// with only pending confirmation text and no actual env/PATH changes
// isn't "empty" from the user's perspective, but it also isn't
// something writeResult (which only ever emits export/JSON for
// EnvVars/PathPrepends) needs to treat specially.
func (r *Result) isEmpty() bool {
	return r == nil || (len(r.EnvVars) == 0 && len(r.PathPrepends) == 0)
}

// joinBanner concatenates two banner strings with a blank line between
// them, omitting either side if empty. Used to combine an
// already-pending banner (e.g. a prerequisite's confirmation) with a
// newly-generated one (e.g. "No JDK selected") when both need to be
// shown together above the same picker.
func joinBanner(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "\n\n" + b
}

// showPendingBanner prints banner directly to sess.Out, with a trailing
// blank line, if it's non-empty. Used specifically for the case where a
// prerequisite's confirmation (or an informational prompt) has nothing
// further to be folded into, because the current resolution path won't
// show its own picker (a direct version argument was given instead).
// Safe to call with an empty banner (no-op). Returns true if it
// actually printed anything -- callers use this to decide whether THEY
// still need to print their own leading blank line before whatever
// they print next (a real, confirmed bug: several call sites assumed
// this function -- or flushMessages below -- had already printed a
// leading blank, which is only true when there was something pending;
// when there wasn't, both are pure no-ops, silently leaving zero
// blank lines instead of the one every other command in this tool
// consistently has).
func showPendingBanner(sess *term.Session, banner string) bool {
	if banner == "" {
		return false
	}
	// Leading blank line too, not just trailing -- this can be the
	// very FIRST thing printed in an entire command's execution (e.g.
	// `sk use maven` recursively resolving a prerequisite `java` that
	// itself has zero versions installed: the banner text IS the
	// first visible output of the whole command, not just of this
	// inner call), so it needs the same leading blank line every other
	// first-printed message in this tool has. Both existing call
	// sites are confirmed safe for this: neither ever has something
	// else already printed immediately before reaching them.
	fmt.Fprintln(sess.Out)
	fmt.Fprintln(sess.Out, banner)
	fmt.Fprintln(sess.Out)
	return true
}

// flushMessages prints any pending result.Messages directly to
// sess.Out (each on its own line, with a leading blank line), then
// clears them. Used immediately before resolveUse is about to print
// its OWN message directly to the console -- ensures an EARLIER event's
// confirmation (e.g. a prerequisite JDK selection) is always shown
// BEFORE a LATER event's message (e.g. "no version selected"),
// preserving correct chronological order (a real bug: "No Maven
// version selected." could otherwise print before "JDK 21.0.2
// selected", even though the JDK was chosen first) -- and ensures
// nothing gets printed again by use.go's final flush afterward.
//
// This is distinct from showPendingBanner/pendingBanner: by the point
// a picker has already run and been cancelled, pendingBanner has
// already been reset to "" (it was consumed as that picker's own
// banner) -- but result.Messages was deliberately preserved through
// that (see the RequiresJava block below), so it's the one that still
// needs flushing here.
//
// Returns true if it actually printed anything -- see
// showPendingBanner's doc comment for why callers need this.
func flushMessages(sess *term.Session, result *Result) bool {
	if len(result.Messages) == 0 {
		return false
	}
	// Exactly one blank line before EACH message, consistently
	// (matches the same rule everywhere else in this tool now) -- the
	// caller (e.g. the cancel branch below) still adds its own
	// separating blank line before whatever it prints next.
	for _, m := range result.Messages {
		fmt.Fprintln(sess.Out)
		fmt.Fprintln(sess.Out, m)
	}
	result.Messages = nil
	return true
}

// resolveUse is the core logic shared by every tool's `use` command --
// interactive picker or direct version argument, the RequiresJava
// prerequisite chain, and not-found handling. The cobra command layer
// (use.go) is a thin wrapper around this: parse flags, call this, print
// the result.
//
// incomingBanner is context accumulated from a PARENT call in the
// RequiresJava chain (e.g. "No JDK selected — choose one first:"),
// meant to be shown as THIS call's own picker's banner, if one ends up
// being shown. Pass "" at the top level (see use.go).
//
// DESIGN NOTE -- the banner/Messages mechanism exists to fix a real UX
// bug: printing a status line and then immediately launching an
// alt-screen picker causes that status line to be visibly wiped out a
// fraction of a second after appearing (alt-screen clears and switches
// buffers). The fix is to never print-then-immediately-launch: pending
// text is either folded into the NEXT picker's own render as a banner
// (so it's part of one coherent screen, never separately flashed), or,
// if no further picker is coming, printed directly and cleanly once
// resolution is fully done.
//
// IMPORTANT: every failure path below returns `result`, not a literal
// nil -- even on error, so that a real prior success (e.g. a JDK
// genuinely selected) is never discarded just because a LATER, separate
// step in the same chain (e.g. Maven's own picker) was cancelled or
// failed.
func resolveUse(sess *term.Session, styles term.Styles, toolName, versionArg, incomingBanner string) (*Result, error) {
	tool, ok := tooldef.Get(toolName)
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", toolName)
	}

	// "default" is a stand-in value, not a separate command shape --
	// it occupies the exact same argument slot a real version would
	// (matching `sk use java 21.0.2`'s own shape, the same convention
	// `docker run image:latest`/`npm install pkg@latest` already use:
	// a special value stays in the position a real one would go,
	// rather than restructuring the whole command around it).
	// Resolved here, once, before anything else -- by the time the
	// rest of this function runs, versionArg is always either a real
	// version or this has already returned.
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
	// Tracks whether ANYTHING has been printed to the console yet in
	// this call -- needed because showPendingBanner/flushMessages are
	// no-ops when there's nothing pending, so a later branch can't
	// just assume one of them already added the leading blank line
	// every other command in this tool consistently has.
	printedSomething := false

	// Checked BEFORE the RequiresJava prerequisite chain below -- a
	// real bug caught via actual use: `sk use maven` with neither
	// Maven nor any JDK installed used to fail with a bare "No JDK
	// selected... No JDK versions found." message, never mentioning
	// Maven at all, since the java prerequisite check ran first and
	// returned before Maven's own inventory was ever examined.
	// There's nothing to activate for Maven if Maven itself has
	// nothing installed, regardless of Java's state -- checking that
	// first means this case now correctly says "No Maven versions
	// found" instead.
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
			// Styled Detail (neutral), not Error -- a real
			// inconsistency found via actual use: this was previously
			// the ONLY "nothing to do" message in the whole project
			// left completely unstyled, while every other command's
			// equivalent ("No X versions found to remove.") was
			// correctly Detail. Nothing installed yet is a "nothing
			// to do" state, not a genuine failure -- see Styles' own
			// doc comment for the full convention.
			fmt.Fprintln(sess.Out, styles.Neutral.Render(fmt.Sprintf("No %s versions found to use.", tool.DisplayName)))
			return result, ErrNotFound
		}
	} else {
		// Same principle as the no-version-given case above, extended
		// to cover a real gap the earlier fix missed: an EXPLICIT
		// version is validated to actually exist BEFORE touching the
		// java prerequisite chain -- there's nothing to activate if
		// the target tool doesn't have THIS SPECIFIC version, no
		// matter what java's state is. A real bug caught via actual
		// use: `sk use maven 3.9.14` with no java installed used to
		// fail with the same confusing bare "No JDK selected... No
		// JDK versions found." message, even though maven itself
		// didn't have 3.9.14 either -- the earlier fix only handled
		// the bare "sk use maven" (no version) case, not this one.
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
				pendingBanner = joinBanner(pendingBanner, strings.Join(result.Messages, "\n"))
				// result.Messages is deliberately NOT cleared here.
				// Keeping it means this confirmation is ALSO printed
				// persistently at the end (use.go's final flush), not
				// just shown transiently as the next picker's banner --
				// fixes a real gap: previously, a real, successful
				// prerequisite selection (e.g. a JDK) could vanish
				// forever once whichever alt-screen it was shown in
				// (as a banner) tore down, with nothing ever reaching
				// the user's actual, persistent terminal scrollback.
			}
			if err != nil {
				return result, err
			}
		}
	}

	if version == "" {
		title := fmt.Sprintf("Select %s version", tool.DisplayName)
		chosen, err := picker.RunGrouped(sess, title, buildPickerGroups(tool, versions), pendingBanner)
		pendingBanner = "" // consumed as this picker's banner regardless of outcome below
		if err != nil {
			return result, err
		}
		if chosen == "" {
			flushMessages(sess, result)
			fmt.Fprintln(sess.Out)
			// Styled Detail (neutral), not Error -- a picker being
			// cancelled is the user's own deliberate choice, not a
			// failure; see Styles' own doc comment for the full
			// convention this follows.
			fmt.Fprintln(sess.Out, styles.Neutral.Render(fmt.Sprintf("No %s version selected to use.", tool.DisplayName)))
			return result, ErrCancelled
		}
		version = chosen
	} else {
		// Direct version arg given -- no picker for THIS tool, so any
		// pending banner (e.g. a prerequisite's confirmation) has
		// nowhere to be folded into; show it directly now instead.
		if showPendingBanner(sess, pendingBanner) {
			printedSomething = true
		}
		pendingBanner = ""
		// Unlike the picker case above, this WAS a real, permanent
		// console print (no alt-screen involved, nothing transient
		// about it) -- so clear result.Messages here specifically, to
		// avoid the final flush in use.go printing the exact same
		// text a second time.
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

	confirmation := styles.Success.Render(
		fmt.Sprintf("\u2713 %s %s — active for this shell session only", tool.DisplayName, version),
	)
	// Subordinate detail line: the actual env var, for anyone who wants
	// to confirm exactly what changed (pointing an IDE at it, general
	// debugging) -- without it dominating the primary confirmation the
	// way the earlier version of this message did (issue #4: it used
	// to show ONLY the raw Contents/Home path as if that were the main
	// point, rather than a supporting detail).
	if tool.EnvVar != "" {
		confirmation += "\n" + styles.Detail.Render(
			fmt.Sprintf("  \u2514\u2500 %s=%s", tool.EnvVar, tool.HomePath(v.Path)),
		)
	}
	result.Messages = append(result.Messages, confirmation)

	return result, nil
}

// clearActive implements `sk use <tool> null` -- deactivates whatever
// is currently active for tool in THIS shell, without touching
// anything on disk. "null" occupies the same argument slot a real
// version or "default" would (see resolveUse's own comment on that
// convention), but short-circuits entirely rather than flowing
// through resolveUse's normal activate-a-version path -- there's
// nothing to pick, install, or chain a prerequisite for when the
// whole point is clearing, not activating.
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

	// binPath stays empty if current doesn't match anything sk
	// recognizes (the same mismatch case `current` handles) -- the
	// env var still gets cleared either way, but there's no known
	// PATH entry to safely strip for something sk can't identify.
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

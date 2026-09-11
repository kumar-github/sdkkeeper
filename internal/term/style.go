package term

import (
	"os"

	"github.com/charmbracelet/lipgloss"
)

// Catppuccin palette, Mocha (dark) paired with its official light-theme
// counterpart Latte, using lipgloss.AdaptiveColor. The Dark side matches
// what this project has used throughout; the Light side is new -- an
// earlier version of this file used fixed, dark-only hex values, which
// would have had genuinely poor contrast in a light-themed terminal
// (pastel colors designed to pop against a dark background read as
// washed-out and hard to read against white). AdaptiveColor makes
// lipgloss query the terminal's REAL, current background at runtime
// (via termenv -- an escape-sequence probe, not a guess) and pick the
// correct side automatically; no configuration needed from the user.
var (
	colorPeach    = lipgloss.AdaptiveColor{Light: "#fe640b", Dark: "#fab387"}
	colorSubtext  = lipgloss.AdaptiveColor{Light: "#6c6f85", Dark: "#a6adc8"}
	colorBlue     = lipgloss.AdaptiveColor{Light: "#1e66f5", Dark: "#89b4fa"}
	colorGreen    = lipgloss.AdaptiveColor{Light: "#40a02b", Dark: "#a6e3a1"}
	colorRed      = lipgloss.AdaptiveColor{Light: "#d20f39", Dark: "#f38ba8"}
	colorYellow   = lipgloss.AdaptiveColor{Light: "#df8e1d", Dark: "#f9e2af"}
	colorBorder   = lipgloss.AdaptiveColor{Light: "#acb0be", Dark: "#585b70"}
	colorLavender = lipgloss.AdaptiveColor{Light: "#7287fd", Dark: "#b4befe"}
	colorMauve    = lipgloss.AdaptiveColor{Light: "#8839ef", Dark: "#cba6f7"}
)

// Styles holds every style the picker and status output need. Built via
// Session.Styles(), deliberately NOT as package-level vars -- see the
// package doc in tty.go for why that ordering matters: package-level
// vars initialize before main() runs, before SetDefaultRenderer in Open()
// has had a chance to apply, and would silently capture the wrong
// renderer the same way the PoC's first attempt at this fix did.
//
// The semantic convention every command in this project follows (a
// real inconsistency found via actual use, across MULTIPLE commands,
// motivated adding this as an explicit, documented rule rather than
// leaving it as an unstated convention new code could easily drift
// from again):
//
//   - Success: a completed action succeeded. Always paired with "✓".
//
//   - Error: something genuinely, unexpectedly went wrong -- a
//     network failure, a permission error, a real conflict (e.g.
//     "already exists"), invalid input (an unknown tool/vendor name).
//     Always paired with "✗".
//
//   - Warning: a real issue, but non-blocking and safe to ignore --
//     e.g. `doctor`'s leftover temp dirs. Always paired with "⚠".
//     Deliberately its OWN style, not a reuse of Error -- a warning
//     that renders identically to a genuine failure defeats the
//     entire point of having two different glyphs.
//
//   - Neutral: a "nothing to do" state that ISN'T a failure -- nothing
//     installed yet, a picker cancelled by the user's own deliberate
//     choice -- specifically when that message is the ENTIRE, sole
//     output of the command, not subordinate to anything else on
//     screen. No glyph -- these aren't failures, so a "✗" would
//     misrepresent them. Deliberately its OWN style, distinct from
//     Detail (a real gap found via actual use: Detail was originally
//     overloaded with two different jobs -- genuinely subordinate
//     text UNDER something more important on screen, like the raw
//     JAVA_HOME=... line under a confirmation, where a muted color
//     correctly signals "secondary" -- and a message that IS the
//     entire output, which was getting the same de-emphasized
//     treatment as a footnote even though it's the only thing being
//     said.
//
//     First fix used Catppuccin's own "Text" tier -- confirmed as a
//     real, distinct color from the same official palette, not
//     invented -- but a real gap found via actual use on a real
//     terminal: on a dark background, "Text" is a near-white color,
//     which reads as indistinguishable from many terminals' own
//     default foreground. Genuinely more readable than Detail's muted
//     "Subtext0", but not RECOGNIZABLE the way Success/Error/Warning
//     are -- it doesn't signal "this is sk's own neutral category" at
//     a glance the same way green/red/amber do for theirs, which was
//     the actual goal. Replaced with Blue -- the well-established,
//     near-universal convention for "informational, neutral" across
//     logging frameworks, linters, and CI tools, the natural fourth
//     member alongside the traffic-light green/red/amber already in
//     use here, chosen specifically for that already-learned
//     recognizability rather than for its own contrast properties.
//
//   - Detail: subordinate/supplementary info under something else
//     already on screen -- e.g. the raw JAVA_HOME=... line under a
//     confirmation, or a progress line during `install`. See Neutral
//     above for the real distinction between the two.
type Styles struct {
	Box      lipgloss.Style
	Title    lipgloss.Style // the picker's own "Select X version" heading
	Active   lipgloss.Style
	Inactive lipgloss.Style
	Success  lipgloss.Style
	Error    lipgloss.Style
	Warning  lipgloss.Style // real but non-blocking issues, e.g. doctor's leftover temp dirs -- see the convention above
	Neutral  lipgloss.Style // a "nothing to do" state that IS the entire output -- see the convention above
	Detail   lipgloss.Style // subordinate info under something else already on screen -- see the convention above
	Header   lipgloss.Style // section headings OUTSIDE the picker, e.g. `list`'s "Managed by SDK Keeper:"
	Default  lipgloss.Style // list's "(default)" tag specifically -- see Styles.Default's own note below
}

// Styles builds a fresh Styles value against this Session's current
// renderer. Call this AFTER Open(), never before.
func (s *Session) Styles() Styles {
	return buildStyles()
}

// StylesForWriter builds a fresh Styles value with lipgloss's color
// detection pointed directly at w, WITHOUT opening /dev/tty at all --
// for the rare, early-lifecycle case where a message must be printed
// reliably to a SPECIFIC, already-known writer (see cli.requireArgs),
// before the normal Open()/Session flow has had any chance to run at
// all.
//
// A real bug caught via actual testing ON A REAL MACHINE -- this
// project's own sandbox has no real terminal at all, so it never
// caught this: cli.requireArgs originally called Open() directly for
// this purpose, which (correctly, by design) opens /dev/tty and
// points Out there whenever a real terminal IS available. On a real
// machine, that meant the message was written to /dev/tty, not
// os.Stderr -- genuinely still visible to the user, but bypassing
// os.Stderr entirely, exactly the property that makes /dev/tty safe
// from accidental shell-eval capture elsewhere in this project. Here,
// though, it meant a test using a standard os.Stderr redirect could
// never observe the message at all, and passed in this sandbox purely
// by coincidence (no real /dev/tty here to bypass anything with).
// Building the renderer directly against the SAME writer the message
// is about to be printed to keeps color detection accurate for where
// the text actually goes, and keeps the message reliably testable
// regardless of whether a real terminal happens to be attached.
func StylesForWriter(w *os.File) Styles {
	lipgloss.SetDefaultRenderer(lipgloss.NewRenderer(w))
	return buildStyles()
}

func buildStyles() Styles {
	return Styles{
		Box: lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(colorBorder).
			Padding(1, 2),
		// Lavender + bold, distinct from every other color in the
		// palette (peach = active, green = success, red = error, muted
		// subtext = inactive) -- so the title reads unmistakably as a
		// heading, not just another line of picker content.
		Title:    lipgloss.NewStyle().Foreground(colorLavender).Bold(true).MarginBottom(1),
		Active:   lipgloss.NewStyle().Foreground(colorPeach).Bold(true),
		Inactive: lipgloss.NewStyle().Foreground(colorSubtext),
		Success:  lipgloss.NewStyle().Foreground(colorGreen).Bold(true),
		Error:    lipgloss.NewStyle().Foreground(colorRed).Bold(true),
		// Amber/yellow -- genuinely distinct from Error's red, so a
		// real bug (doctor's own "⚠" glyph was previously rendered in
		// THE SAME red as "✗", making a cosmetic, safe-to-ignore
		// issue visually indistinguishable from an actual failure) is
		// actually fixed, not just relabeled.
		Warning: lipgloss.NewStyle().Foreground(colorYellow).Bold(true),
		// Blue -- the near-universal "informational, neutral"
		// convention (logging frameworks, linters, CI tools), chosen
		// specifically for that already-learned recognizability, the
		// same reason green/red/amber were chosen for their own
		// categories -- see the Styles doc comment above for the
		// full reasoning, including why Catppuccin's own "Text" tier
		// (this style's first version) didn't actually achieve that:
		// a near-white color on a dark background reads as
		// indistinguishable from many terminals' own default
		// foreground, more readable than Detail's muted grey but not
		// RECOGNIZABLE the way this category needed to be. Not bold:
		// "neutral" specifically means not emphasized the way
		// Success/Error/Warning are -- bolding it would visually
		// compete with those three for the same urgency, undermining
		// the whole point of a fourth, calmer category.
		Neutral: lipgloss.NewStyle().Foreground(colorBlue),
		// Same muted color as Inactive, but named for its own distinct
		// purpose (supplementary detail text, not a picker item) --
		// kept as a separate style rather than reusing Inactive so the
		// call site reads clearly and isn't misleading later.
		Detail: lipgloss.NewStyle().Foreground(colorSubtext),
		// Same lavender+bold visual language as Title (consistent
		// "this is a heading" language across the whole tool), but
		// WITHOUT Title's MarginBottom(1) -- that margin exists for
		// the picker's spacing needs specifically (blank line before
		// its list of items) and would introduce an unwanted blank
		// line in `list`'s tighter, already-established format if
		// Title were reused here directly.
		Header: lipgloss.NewStyle().Foreground(colorLavender).Bold(true),
		// `list`'s "(default)" tag specifically -- a real gap found
		// via actual use: "(current)" and "(default)" were both
		// completely UNSTYLED plain text, identical in appearance,
		// making them hard to tell apart at a glance. "(current)"
		// reuses Active (peach) directly -- the exact same "this is
		// the live one" meaning the picker's own highlighted-item
		// color already carries.
		//
		// First fix used non-bold Lavender -- a real, separate gap
		// found via actual use: paired with Header/Title's OWN bold
		// lavender, the non-bold weight (chosen specifically to avoid
		// competing with a section heading) ended up reading as
		// washed-out and low-priority next to (current)'s bold peach,
		// not just "differently colored". Replaced with bold Mauve --
		// genuinely distinct from Lavender (still the same general
		// purple family as Header/Title, so it still reads as "an sk
		// label", just not confusable with either Header/Title or
		// Peach), and bold to match (current)'s own visual weight
		// rather than reading as secondary to it.
		Default: lipgloss.NewStyle().Foreground(colorMauve).Bold(true),
	}
}

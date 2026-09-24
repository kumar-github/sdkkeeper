package term

import (
	"os"

	"github.com/charmbracelet/lipgloss"
)

// Catppuccin palette, Mocha (dark) paired with Latte (light), using
// lipgloss.AdaptiveColor so lipgloss queries the terminal's real
// current background at runtime and picks the correct side
// automatically.
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

// Styles holds every style the picker and status output need. Built
// via Session.Styles(), not as package-level vars -- package-level
// vars initialize before main() runs, before Open()'s
// SetDefaultRenderer has applied, and would capture the wrong
// renderer.
//
// The semantic convention every command follows:
//
//   - Success: a completed action succeeded. Always paired with "✓".
//   - Error: something genuinely went wrong -- network failure,
//     permission error, invalid input. Always paired with "✗".
//   - Warning: a real but non-blocking issue, e.g. doctor's leftover
//     temp dirs. Always paired with "⚠". Its own style, not a reuse
//     of Error -- otherwise a warning would look like a real failure.
//   - Neutral: a "nothing to do" state that isn't a failure (nothing
//     installed yet, a picker cancelled deliberately), when that
//     message is the entire output, not subordinate to anything
//     else. No glyph. Its own style, distinct from Detail, which is
//     for text subordinate to something else already on screen.
//     Uses Blue, the common "informational, neutral" convention
//     across logging/CI tools, chosen for recognizability rather
//     than contrast.
//   - Detail: subordinate/supplementary info under something else
//     on screen, e.g. the raw JAVA_HOME=... line under a
//     confirmation, or a progress line during install.
type Styles struct {
	Box      lipgloss.Style
	Title    lipgloss.Style // the picker's own "Select X version" heading
	Active   lipgloss.Style
	Inactive lipgloss.Style
	Success  lipgloss.Style
	Error    lipgloss.Style
	Warning  lipgloss.Style // real but non-blocking issues -- see the convention above
	Neutral  lipgloss.Style // a "nothing to do" state that IS the entire output
	Detail   lipgloss.Style // subordinate info under something else on screen
	Header   lipgloss.Style // section headings outside the picker, e.g. `list`'s "Managed by SDK Keeper:"
	Default  lipgloss.Style // list's "(default)" tag specifically
	External lipgloss.Style // list --sizes's "Not managed by SDK Keeper" rows specifically
}

// Styles builds a fresh Styles value against this Session's current
// renderer. Call this after Open(), never before.
func (s *Session) Styles() Styles {
	return buildStyles()
}

// StylesForWriter builds a fresh Styles value with lipgloss's color
// detection pointed directly at w, without opening /dev/tty -- for the
// rare, early-lifecycle case where a message must be printed reliably
// to a specific, already-known writer (see cli.requireArgs), before
// the normal Open()/Session flow has run. Building the renderer
// against the same writer the message is printed to keeps color
// detection accurate for where the text actually goes, and keeps the
// message testable regardless of whether a real terminal is attached.
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
		// palette, so the title reads unmistakably as a heading.
		Title:    lipgloss.NewStyle().Foreground(colorLavender).Bold(true).MarginBottom(1),
		Active:   lipgloss.NewStyle().Foreground(colorPeach).Bold(true),
		Inactive: lipgloss.NewStyle().Foreground(colorSubtext),
		Success:  lipgloss.NewStyle().Foreground(colorGreen).Bold(true),
		Error:    lipgloss.NewStyle().Foreground(colorRed).Bold(true),
		Warning:  lipgloss.NewStyle().Foreground(colorYellow).Bold(true),
		// Not bold: "neutral" means not emphasized the way
		// Success/Error/Warning are.
		Neutral: lipgloss.NewStyle().Foreground(colorBlue),
		// Same muted color as Inactive, kept as its own style so the
		// call site reads clearly (supplementary text, not a picker item).
		Detail: lipgloss.NewStyle().Foreground(colorSubtext),
		// Same lavender+bold as Title, but without its MarginBottom
		// -- that spacing is for the picker's needs, not `list`'s
		// tighter format.
		Header: lipgloss.NewStyle().Foreground(colorLavender).Bold(true),
		// Bold Mauve: distinct from Header/Title's Lavender (still
		// the same purple family, reads as "an sk label") and from
		// Active's Peach, matching (current)'s own bold visual weight.
		Default: lipgloss.NewStyle().Foreground(colorMauve).Bold(true),
		// Same muted color as Detail, but italicized -- so `sk list
		// --sizes`'s "Not managed by SDK Keeper" rows read as
		// visually distinct at a glance, not just via their header
		// text on careful reading. Same reasoning as Warning being
		// its own style rather than reusing Error: the visual
		// difference itself is the point, not just the label.
		External: lipgloss.NewStyle().Foreground(colorSubtext).Italic(true),
	}
}

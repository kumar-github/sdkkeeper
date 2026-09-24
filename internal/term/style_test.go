package term

import "testing"

// TestStyles_ExternalIsItalicDetailIsNot confirms `sk list --sizes`'s
// "Not managed by SDK Keeper" rows render in a style that's visually
// distinct from ordinary subordinate text (Detail), not just
// distinguished by their header's own wording. Checked directly
// against the style's own configured property, not rendered ANSI
// output -- piped/non-TTY output has color disabled entirely (correct
// behavior), so there's no escape code to inspect there; the style
// definition itself is the right thing to assert against.
func TestStyles_ExternalIsItalicDetailIsNot(t *testing.T) {
	s := buildStyles()
	if !s.External.GetItalic() {
		t.Error("expected Styles.External to be italic")
	}
	if s.Detail.GetItalic() {
		t.Error("expected Styles.Detail to NOT be italic -- External must be visually distinct from it")
	}
}

package cli

import (
	"fmt"
	"strings"
)

// renderDownloadProgress builds the hybrid progress line -- a bar,
// percentage, and byte counts together, e.g.
// "[####################................] 58% (30.2 MB / 52.1 MB)".
// If total is unknown (-1, server didn't report Content-Length), falls
// back to just showing bytes read so far -- a percentage or a bar
// implies a known target, and showing one against an unknown total
// would be actively misleading, not just incomplete.
func renderDownloadProgress(read, total int64) string {
	if total <= 0 {
		// Composes as "Downloading X.X MB" at the call site -- no
		// trailing "downloaded" here, which would otherwise read
		// redundantly once prefixed ("Downloading 23.4 MB downloaded").
		return formatMB(read)
	}

	const barWidth = 36
	filled := int(float64(barWidth) * float64(read) / float64(total))
	if filled > barWidth {
		filled = barWidth
	}
	bar := strings.Repeat("#", filled) + strings.Repeat(".", barWidth-filled)
	percent := float64(read) / float64(total) * 100

	return fmt.Sprintf("[%s] %.0f%% (%s / %s)", bar, percent, formatMB(read), formatMB(total))
}

func formatMB(b int64) string {
	return fmt.Sprintf("%.1f MB", float64(b)/(1024*1024))
}

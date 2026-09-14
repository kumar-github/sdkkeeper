package cli

import (
	"fmt"
	"strings"
	"time"
)

// renderDownloadProgress builds the hybrid progress line -- a bar,
// percentage, byte counts, and now speed/ETA together, e.g.
// "[####################................] 58% (30.2 MB / 52.1 MB) 2.3 MB/s, ETA 9s".
// If total is unknown (-1, server didn't report Content-Length), falls
// back to just showing bytes read so far (plus speed, if computable)
// -- a percentage or a bar implies a known target, and showing one
// against an unknown total would be actively misleading, not just
// incomplete. ETA specifically also needs a known total (there's
// nothing to estimate time "until" otherwise), so it's omitted there
// even though speed alone still is shown.
//
// elapsed is the time since the download started -- passed in rather
// than computed here, since this function has no notion of "when did
// this download begin" on its own; the caller (install.go) is what
// tracks that, the same way it already tracks downloadedBytes.
//
// Known, deliberately accepted imprecision for a RESUMED download
// specifically (see downloadOnce's own resume-support comment): read
// includes bytes that were already on disk from a prior, failed
// attempt, while elapsed only covers THIS attempt's own time -- so
// speed briefly overstates itself right after a resume begins. Not
// fixed with extra parameters/complexity here deliberately: it's
// purely cosmetic (the bar/percentage/ETA are still correct either
// way), and self-corrects within a few seconds as newly-downloaded
// bytes come to dominate the average.
func renderDownloadProgress(read, total int64, elapsed time.Duration) string {
	if total <= 0 {
		// Composes as "Downloading X.X MB" at the call site -- no
		// trailing "downloaded" here, which would otherwise read
		// redundantly once prefixed ("Downloading 23.4 MB downloaded").
		line := formatMB(read)
		if speed := formatSpeed(read, elapsed); speed != "" {
			line += " " + speed
		}
		return line
	}

	const barWidth = 36
	filled := int(float64(barWidth) * float64(read) / float64(total))
	if filled > barWidth {
		filled = barWidth
	}
	bar := strings.Repeat("#", filled) + strings.Repeat(".", barWidth-filled)
	percent := float64(read) / float64(total) * 100

	line := fmt.Sprintf("[%s] %.0f%% (%s / %s)", bar, percent, formatMB(read), formatMB(total))
	if eta := formatSpeedAndETA(read, total, elapsed); eta != "" {
		line += " " + eta
	}
	return line
}

// formatSpeed returns "X.X MB/s", or "" if not yet computable (no
// bytes read yet, or no meaningful time has elapsed -- avoiding a
// divide-by-zero AND avoiding a wildly inflated, meaningless rate
// from a near-zero elapsed duration on the very first progress tick).
func formatSpeed(read int64, elapsed time.Duration) string {
	if read <= 0 || elapsed < 100*time.Millisecond {
		return ""
	}
	bytesPerSec := float64(read) / elapsed.Seconds()
	return fmt.Sprintf("%.1f MB/s", bytesPerSec/(1024*1024))
}

// formatSpeedAndETA returns "X.X MB/s, ETA Ys" (or "Xm Ys" past a
// minute), or "" if not yet computable -- same guards as formatSpeed,
// plus total must be known (an ETA is meaningless without a target to
// estimate time "until").
func formatSpeedAndETA(read, total int64, elapsed time.Duration) string {
	speed := formatSpeed(read, elapsed)
	if speed == "" || total <= 0 || read >= total {
		return speed
	}
	bytesPerSec := float64(read) / elapsed.Seconds()
	remaining := float64(total-read) / bytesPerSec
	return fmt.Sprintf("%s, ETA %s", speed, formatDuration(remaining))
}

// formatDuration renders a number of seconds as "Ys" under a minute,
// or "Xm Ys" at or past one -- ETAs longer than that are common
// enough for a slow connection or a large JDK that "127s" would be a
// genuinely harder number to read at a glance than "2m 7s".
func formatDuration(seconds float64) string {
	total := int(seconds + 0.5) // round to nearest second
	if total < 60 {
		return fmt.Sprintf("%ds", total)
	}
	return fmt.Sprintf("%dm %ds", total/60, total%60)
}

func formatMB(b int64) string {
	return fmt.Sprintf("%.1f MB", float64(b)/(1024*1024))
}

package cli

import (
	"fmt"
	"strings"
	"time"
)

// renderDownloadProgress builds the progress line: a bar, percentage,
// byte counts, and speed/ETA, e.g. "[####...] 58% (30.2 MB / 52.1 MB)
// 2.3 MB/s, ETA 9s". If total is unknown, falls back to just bytes
// read plus speed -- a bar/percentage against an unknown total would
// be misleading. elapsed is passed in since this function has no
// notion of when the download started; install.go tracks that.
//
// For a resumed download, read includes bytes already on disk from a
// prior attempt while elapsed only covers this attempt, so speed
// briefly overstates itself -- cosmetic only, and self-corrects
// within a few seconds.
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

// formatSpeed returns "X.X MB/s", or "" if not yet computable --
// avoids a divide-by-zero and a wildly inflated rate from a near-zero
// elapsed duration on the first progress tick.
func formatSpeed(read int64, elapsed time.Duration) string {
	if read <= 0 || elapsed < 100*time.Millisecond {
		return ""
	}
	bytesPerSec := float64(read) / elapsed.Seconds()
	return fmt.Sprintf("%.1f MB/s", bytesPerSec/(1024*1024))
}

// formatSpeedAndETA returns "X.X MB/s, ETA Ys", or "" if not yet
// computable -- same guards as formatSpeed, plus total must be known.
func formatSpeedAndETA(read, total int64, elapsed time.Duration) string {
	speed := formatSpeed(read, elapsed)
	if speed == "" || total <= 0 || read >= total {
		return speed
	}
	bytesPerSec := float64(read) / elapsed.Seconds()
	remaining := float64(total-read) / bytesPerSec
	return fmt.Sprintf("%s, ETA %s", speed, formatDuration(remaining))
}

// formatDuration renders seconds as "Ys" under a minute, or "Xm Ys"
// past one.
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

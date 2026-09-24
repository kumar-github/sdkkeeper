package cli

import (
	"fmt"
	"strings"
	"time"
)

// renderDownloadProgress builds the progress line: a bar, percentage,
// byte counts, and speed/ETA, e.g. "[████░░░░] 58% (30.2 MB / 52.1 MB)
// 2.3 MB/s, ETA 9s" -- Unicode block characters, not plain ASCII,
// matching the same style `sk list --sizes`'s own per-entry bars use.
// If total is unknown, falls back to just bytes
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
	//bar := strings.Repeat("\u2588", filled) + strings.Repeat("\u2591", barWidth-filled)
	bar := strings.Repeat("\u2501", filled) + strings.Repeat("\u2500", barWidth-filled)
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

// spinnerFrames is a standard Braille-dot spinner -- the same visual
// convention most modern CLIs (npm, cargo, etc.) use for "something
// indeterminate is happening", recognizable without needing to read
// the accompanying text closely.
var spinnerFrames = []string{"\u280b", "\u2819", "\u2839", "\u2838", "\u283c", "\u2834", "\u2826", "\u2827", "\u2807", "\u280f"}

// runWithSpinner runs work while animating a spinner next to message,
// redrawn in place -- for a genuinely indeterminate wait with no
// natural progress callback of its own (a single blocking vendor API
// call, unlike the download/extraction progress above, which already
// gets real, incremental callbacks from installer.Install itself).
// Two real, previously silent gaps this fixes: `sk install`'s own
// "Resolving..." line used to just sit there with zero live
// indication anything was happening for however long the vendor API
// took to respond; the picker's own major/patch-version listing calls
// had NO status message AT ALL before this, not even a static one.
//
// Only animates when session.HasTTY -- a spinner with no real cursor
// to redraw with is just log spam, worse than a single static line.
// On a non-TTY session, prints message once (unstyled by this
// function; the caller's own message string carries its own styling)
// and runs work synchronously, matching exactly what every one of
// these three call sites already did before this existed.
//
// Checksum verification was considered for the same treatment and
// deliberately left out: it's computed INCREMENTALLY as the download
// streams in (see installer.go's own hasher.Write calls during the
// copy loop), not as a separate blocking pass afterward -- the final
// comparison is one instant Sum(nil) call, not something a spinner
// would ever actually have time to animate against.
// minSpinnerDuration is the shortest time runWithSpinner keeps
// animating once started, even if work() finishes sooner -- without
// this, a fast resolve (a single HTTP call, easily well under 80ms on
// a good connection) draws and erases the spinner within a single
// terminal repaint: genuinely too fast to register, indistinguishable
// from nothing happening at all. Confirmed directly, not just
// reasoned about: a real BellSoft resolve completing before the
// first tick left nothing visible on screen at all, jumping straight
// to the next line. 300ms is long enough to be clearly perceptible,
// short enough not to feel like an artificial delay -- the same
// rough range common loading-spinner conventions elsewhere use.
const minSpinnerDuration = 300 * time.Millisecond

func runWithSpinner(message, doneMessage string, work func() error) error {
	if !session.HasTTY {
		fmt.Fprintln(session.Out, styles.Detail.Render(message))
		err := work()
		if err == nil {
			// Matches Downloading/Extracting's own non-TTY behavior --
			// each prints its own final "complete" line too (see
			// DownloadProgress/ExtractionProgress below), not just
			// the in-progress message with nothing after it.
			fmt.Fprintln(session.Out, styles.Detail.Render(doneMessage))
		}
		return err
	}

	start := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- work()
	}()

	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	frame := 0
	redraw := func() {
		fmt.Fprintf(session.Out, "\r\033[K%s", styles.Detail.Render(spinnerFrames[frame]+" "+message))
	}
	redraw()

	// finish clears the animated line, then -- only on success --
	// replaces it with a PERSISTED completion line, matching
	// Downloading's "Download complete ..." and Extracting's
	// "Extraction complete (N files)": both stay visible once done,
	// not erased with no trace. On failure, this only clears (no
	// doneMessage), leaving a clean line for the caller's own error
	// message that follows -- matching Downloading/Extracting's own
	// error path, which clears any redrawn line first for the exact
	// same reason (see install.go's own "Clears any still-active
	// redrawn progress line" comment).
	finish := func(err error) error {
		fmt.Fprint(session.Out, "\r\033[K")
		if err == nil {
			fmt.Fprintln(session.Out, styles.Detail.Render(doneMessage))
		}
		return err
	}

	var workErr error
	workDone := false
	for {
		select {
		case err := <-done:
			workErr = err
			workDone = true
			if time.Since(start) >= minSpinnerDuration {
				return finish(workErr)
			}
			// Keep animating (via the ticker cases below) until the
			// floor is reached -- work has already finished, but
			// finishing right now is exactly the flash-of-nothing
			// minSpinnerDuration exists to prevent.
		case <-ticker.C:
			frame = (frame + 1) % len(spinnerFrames)
			redraw()
			if workDone && time.Since(start) >= minSpinnerDuration {
				return finish(workErr)
			}
		}
	}
}

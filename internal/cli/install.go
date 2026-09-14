package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"sdkkeeper/internal/installer"
	"sdkkeeper/internal/registry"
	"sdkkeeper/internal/tooldef"
)

// alreadyExistsMessage builds the "already installed" error, showing
// the REAL, resolved location when targetDir is a symlink (an
// add-registered entry) -- reuses resolveSymlinkPath, the same shared
// resolution helpers.displayPath itself builds on, rather than a
// second, separate copy of the same os.Readlink logic.
func alreadyExistsMessage(prefix, versionLabel, targetDir string) string {
	real := resolveSymlinkPath(targetDir)
	if real != targetDir {
		return fmt.Sprintf("\u2717 %s%s already installed at %s (not managed by SDK Keeper)", prefix, versionLabel, real)
	}
	return fmt.Sprintf("\u2717 %s%s already installed at %s", prefix, versionLabel, targetDir)
}

func newInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install <tool> [version-vendor]",
		Short: "Download and install a new version of a tool",
		Example: `  sk install java 21.0.2-temurin    # exact version+vendor, no pickers
  sk install java 21.0.2-liberica   # exact version+vendor, no pickers
  sk install java                    # vendor picker, then major picker, then patch picker`,
		Args: requireArgs(cobra.RangeArgs(1, 2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			toolName := args[0]
			arg := ""
			if len(args) == 2 {
				arg = args[1]
			}

			tool, err := requireTool(toolName)
			if err != nil {
				return err
			}

			// Generic check: does this tool have ANY registered
			// provider at all? Rather than a hardcoded "is this
			// java?" check, this automatically produces correct
			// behavior for every future tool the moment its own
			// provider is registered in providers.go, with zero
			// changes needed here.
			if len(vendorNamesFor(tool.Name)) == 0 {
				fmt.Fprintln(session.Out)
				fmt.Fprintln(session.Out, styles.Error.Render(
					fmt.Sprintf("\u2717 install is not yet supported for %s — currently supported: %s", tool.DisplayName, strings.Join(installSupportedTools(), ", ")),
				))
				return fmt.Errorf("install not yet supported for %s", tool.Name)
			}

			ctx := cmd.Context()

			var provider registry.Provider
			var version string

			if v, vendorName, matched := parseFullIdentifier(tool.Name, arg); matched {
				version = v
				provider = providersFor(tool.Name)[vendorName]
			} else {
				var err error
				provider, version, err = resolveVendorAndVersion(ctx, tool)
				if err != nil {
					return err
				}
			}

			// The version LABEL used for the folder name -- and for
			// any command the user needs to type to activate this
			// install afterward -- includes the vendor suffix ONLY
			// when the tool genuinely has more than one vendor (see
			// hasSingleVendor). For java, this is deliberate,
			// unchanged behavior: every install is explicit about
			// which vendor it came from, with no silent default and
			// no bare, unsuffixed naming going forward. For a
			// single-vendor tool (e.g. Maven), a suffix would be
			// pure redundancy -- its own FolderPrefix already bakes
			// in its one vendor ("apache-maven-"), so
			// "apache-maven-3.9.14-apache" would be actively
			// confusing, not more informative. Existing bare-named
			// java installs from before vendor suffixes existed at
			// all keep working unchanged either way (`list`/`use`
			// just pattern-match the "JDK-" prefix regardless of
			// what follows).
			versionLabel := version
			if !hasSingleVendor(tool.Name) {
				versionLabel = version + "-" + provider.Name()
			}
			targetDir := filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+versionLabel)

			// Checked here, BEFORE any network activity, so a
			// redundant install of an already-installed version fails
			// instantly rather than wasting a network round-trip.
			// installer.Install performs the same check again right
			// before writing (the authoritative, race-safe guard).
			if _, err := os.Lstat(targetDir); err == nil {
				fmt.Fprintln(session.Out)
				fmt.Fprintln(session.Out, styles.Error.Render(
					alreadyExistsMessage(tool.FolderPrefix, versionLabel, targetDir),
				))
				return fmt.Errorf("already exists")
			}

			fmt.Fprintln(session.Out)
			fmt.Fprintln(session.Out, styles.Detail.Render(
				fmt.Sprintf("Resolving %s %s (%s)...", tool.DisplayName, version, provider.Name()),
			))

			asset, err := provider.ResolveAsset(ctx, version, runtime.GOOS, runtime.GOARCH)
			if err != nil {
				if err == registry.ErrVersionNotFound {
					fmt.Fprintln(session.Out, styles.Error.Render(
						fmt.Sprintf("\u2717 %s %s not found for %s/%s via %s", tool.DisplayName, version, runtime.GOOS, runtime.GOARCH, provider.Name()),
					))
					return fmt.Errorf("version not found")
				}
				fmt.Fprintln(session.Out, styles.Error.Render(
					fmt.Sprintf("\u2717 Could not resolve %s %s: %s", tool.DisplayName, version, err),
				))
				return err
			}

			// Shows the REAL, exact archive name the vendor resolved
			// to -- the "Resolving..." line above is necessarily
			// forward-looking (it prints BEFORE this call, so it
			// can't show what hasn't been discovered yet); this one
			// shows what was actually found, including the true
			// build number that the bare version alone doesn't
			// capture. Shown as the vendor's own, verbatim filename --
			// not reformatted -- so it's directly verifiable against
			// what the vendor itself calls it.
			fmt.Fprintln(session.Out, styles.Detail.Render(
				fmt.Sprintf("Found: %s", asset.Filename),
			))

			// Non-TTY milestone tracking for DownloadProgress below --
			// see its own comment for why this exists (avoiding
			// SDKMAN's own documented log-spam problem: a redraw-in-
			// place progress line becomes hundreds of separate lines
			// when not connected to a real terminal, since there's no
			// real cursor to move back).
			lastMilestone := -1
			// Captured from inside DownloadProgress below (on the
			// final call) so the actual downloaded size can be
			// included in the confirmation message afterward --
			// avoids a second, redundant size lookup once the
			// download is already done.
			var downloadedBytes int64
			var downloadStart time.Time

			err = installer.Install(ctx, installer.Options{
				URL:               asset.URL,
				Filename:          asset.Filename,
				Checksum:          asset.Checksum,
				ChecksumAlgorithm: asset.ChecksumAlgorithm,
				TargetDir:         targetDir,
				TempRoot:          tooldef.TempRoot(),
				Progress: func(msg string) {
					fmt.Fprintln(session.Out, styles.Detail.Render(msg))
				},
				DownloadProgress: func(read, total int64, final bool) {
					if downloadStart.IsZero() {
						downloadStart = time.Now()
					}
					if final {
						downloadedBytes = read
					}
					// "Downloading" (no "...") prefixed directly onto
					// the bar itself, one line, not a separate
					// announcement beforehand -- the bar's own motion
					// already signals "in progress" more precisely
					// than an ellipsis would, so the ellipsis is
					// redundant once the bar is right there. Unlike
					// "Resolving..."/"Extracting...", which keep their
					// own "..." since neither has a progress
					// indicator following it.
					// On the FINAL call, the prefix switches to "Download
					// complete" -- matches the exact wording browsers/download
					// managers already use for this moment (Chrome/Firefox
					// both say "Download complete", not "Downloaded"), so
					// it's immediately familiar rather than a new phrase to
					// parse. No ellipsis on this one: an ellipsis after
					// "complete" would read as contradictory, a finished
					// state written to look unfinished.
					prefix := "Downloading "
					if final {
						prefix = "Download complete "
					}
					line := prefix + renderDownloadProgress(read, total, time.Since(downloadStart))
					if session.HasTTY {
						// Redraw in place: \r returns to column 0,
						// \033[K erases anything left over from a
						// previous, longer line (e.g. total shrinking
						// the bar width isn't possible here, but the
						// byte-count text itself grows as read grows,
						// so this guards against any stale trailing
						// characters regardless).
						fmt.Fprintf(session.Out, "\r\033[K%s", styles.Detail.Render(line))
						if final {
							fmt.Fprintln(session.Out) // move off this line for whatever prints next
						}
						return
					}
					// Not a real terminal -- never redraw (there's no
					// real cursor to move back), and only print at
					// 25% milestones, each its own line, to avoid
					// exactly the "hundreds of lines of progress
					// output" problem confirmed in SDKMAN's own real,
					// documented GitHub issues (#982) when its
					// curl-based progress bar hits a non-TTY context
					// like a CI log.
					if total <= 0 {
						// No known total means no meaningful
						// percentage milestone to track -- but still
						// show SOMETHING once, at the very end, rather
						// than silently printing nothing for the
						// entire download in a log. Never printed on
						// intermediate ticks here (that would
						// reintroduce the exact spam problem this
						// whole branch exists to avoid).
						if final {
							fmt.Fprintln(session.Out, styles.Detail.Render(line))
						}
						return
					}
					milestone := int(float64(read)/float64(total)*100/25) * 25
					if milestone > lastMilestone || final {
						fmt.Fprintln(session.Out, styles.Detail.Render(line))
						lastMilestone = milestone
					}
				},
				ExtractionProgress: func(count int64, final bool) {
					// No known total for extraction (tar archives
					// don't declare an entry count upfront the way
					// HTTP declares Content-Length -- counting would
					// mean reading the whole archive twice just to
					// find out), so this is always an open-ended
					// count, never a percentage or bar.
					//
					// Unlike DownloadProgress, this closure doesn't
					// need its own "is this worth showing" check --
					// the installer package (see its own
					// crossedThreshold comment) only calls this at
					// all once extraction has already proven to be
					// genuinely slow, so by the time we're here,
					// showing something is already the right call.
					//
					// On the FINAL call, this switches to "Extraction
					// complete" (no "..."), matching the same treatment
					// as the download bar's own "Download complete" --
					// consistency between the two progress indicators
					// matters more than whether a file count "feels"
					// as meaningful to leave visible as a byte count
					// does; an earlier version of this cleared the line
					// entirely instead, which read as inconsistent next
					// to the download bar's own persistent 100% state.
					line := fmt.Sprintf("Extracting... (%d files)", count)
					if final {
						line = fmt.Sprintf("Extraction complete (%d files)", count)
					}
					if session.HasTTY {
						fmt.Fprintf(session.Out, "\r\033[K%s", styles.Detail.Render(line))
						if final {
							fmt.Fprintln(session.Out) // move off this line for whatever prints next
						}
						return
					}
					// Not a real terminal -- never redraw, and (since
					// there's no percentage to gate milestones on)
					// only print once, at the final count, avoiding
					// per-tick log spam the same way the
					// unknown-total download case does.
					if final {
						fmt.Fprintln(session.Out, styles.Detail.Render(line))
					}
				},
			})
			if err != nil {
				if err == installer.ErrAlreadyExists {
					fmt.Fprintln(session.Out, styles.Error.Render(
						alreadyExistsMessage(tool.FolderPrefix, versionLabel, targetDir),
					))
					return fmt.Errorf("already exists")
				}
				// Clear any still-active redrawn progress line before
				// printing the error -- if download or extraction
				// failed PARTWAY through (not the common success
				// path), the last redrawn line was never given a
				// final=true call to clean itself up, and without
				// this, the error text could render appended to the
				// end of a stale progress line instead of on its own
				// clean line. Harmless no-op if nothing was actually
				// being redrawn (a plain terminal control sequence,
				// not visible text).
				if session.HasTTY {
					fmt.Fprint(session.Out, "\r\033[K")
				}
				fmt.Fprintln(session.Out, styles.Error.Render(
					fmt.Sprintf("\u2717 Install failed: %s", err),
				))
				return err
			}

			// Blank line here specifically -- separates the outcome
			// from the Resolving/Downloading/Extracting progress
			// narrative above it, which deliberately stays tight
			// against itself (those are one continuously-evolving
			// status stream, not separate distinct events -- unlike
			// this final confirmation, which is a genuinely different,
			// concluding message).
			fmt.Fprintln(session.Out)
			fmt.Fprintln(session.Out, styles.Success.Render(
				fmt.Sprintf("\u2713 %s %s (%s, %s) installed — run `sk use %s %s` to activate it", tool.DisplayName, version, provider.Name(), formatMB(downloadedBytes), tool.Name, versionLabel),
			))
			return nil
		},
	}
}

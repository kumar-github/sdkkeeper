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
// the real, resolved location when targetDir is a symlink (an
// add-registered entry).
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

			// The vendor suffix is added only for a tool with more
			// than one vendor (see hasSingleVendor) -- a single-vendor
			// tool's FolderPrefix already bakes in its one vendor
			// (e.g. "apache-maven-"), so a suffix would be redundant.
			versionLabel := version
			if !hasSingleVendor(tool.Name) {
				versionLabel = version + "-" + provider.Name()
			}
			targetDir := filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+versionLabel)

			// Checked before any network activity; installer.Install
			// repeats this check right before writing as the
			// authoritative, race-safe guard.
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

			// The vendor's own, verbatim filename -- not reformatted --
			// so it's directly verifiable, including the true build
			// number the bare version alone doesn't capture.
			fmt.Fprintln(session.Out, styles.Detail.Render(
				fmt.Sprintf("Found: %s", asset.Filename),
			))

			// Tracks 25%-milestone printing for non-TTY output below,
			// avoiding a redraw-in-place bar turning into hundreds of
			// separate lines with no real cursor to move.
			lastMilestone := -1
			// Captured on DownloadProgress's final call so the actual
			// size can go in the confirmation message without a
			// second lookup.
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
					// No ellipsis: the bar's own motion already signals
					// "in progress". On the final call, matches
					// browsers' own "Download complete" wording.
					prefix := "Downloading "
					if final {
						prefix = "Download complete "
					}
					line := prefix + renderDownloadProgress(read, total, time.Since(downloadStart))
					if session.HasTTY {
						// \r + \033[K: redraw in place, clearing any
						// stale trailing characters from a longer line.
						fmt.Fprintf(session.Out, "\r\033[K%s", styles.Detail.Render(line))
						if final {
							fmt.Fprintln(session.Out)
						}
						return
					}
					// No real cursor to redraw with outside a TTY, so
					// print only at 25% milestones to avoid hundreds
					// of lines of log spam.
					if total <= 0 {
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
					// No known total (tar doesn't declare an entry
					// count upfront), so always an open-ended count,
					// never a percentage. installer only calls this
					// once extraction has proven genuinely slow.
					line := fmt.Sprintf("Extracting... (%d files)", count)
					if final {
						line = fmt.Sprintf("Extraction complete (%d files)", count)
					}
					if session.HasTTY {
						fmt.Fprintf(session.Out, "\r\033[K%s", styles.Detail.Render(line))
						if final {
							fmt.Fprintln(session.Out)
						}
						return
					}
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
				// Clears any still-active redrawn progress line (never
				// given a final=true call if it failed partway
				// through), so the error doesn't render appended to it.
				if session.HasTTY {
					fmt.Fprint(session.Out, "\r\033[K")
				}
				fmt.Fprintln(session.Out, styles.Error.Render(
					fmt.Sprintf("\u2717 Install failed: %s", err),
				))
				return err
			}

			// Separates the outcome from the tight Resolving/
			// Downloading/Extracting status stream above it.
			fmt.Fprintln(session.Out)
			fmt.Fprintln(session.Out, styles.Success.Render(
				fmt.Sprintf("\u2713 %s %s (%s, %s) installed — run `sk use %s %s` to activate it", tool.DisplayName, version, provider.Name(), formatMB(downloadedBytes), tool.Name, versionLabel),
			))
			return nil
		},
	}
}

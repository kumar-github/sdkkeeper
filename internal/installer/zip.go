package installer

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// extractZip extracts a .zip archive into destDir -- the archive
// format both Temurin and Liberica publish their Windows JDK builds
// as (confirmed during this project's earlier live-API research into
// both vendors), as opposed to the .tar.gz format used on
// darwin/linux. Mirrors extractTarGz's exact signature, progress
// callback contract, and Zip-Slip path-traversal guard -- deliberately
// kept as close to a line-for-line structural match as the two
// archive formats' real API differences allow, so a future reader
// already familiar with one immediately understands the other.
//
// Real, confirmed difference from extractTarGz's implementation:
// archive/zip's Reader gives the full entry list up front (r.File, a
// slice), unlike archive/tar's Reader, which is a genuinely streaming,
// one-entry-at-a-time API (tr.Next()) -- zip's own central directory
// format is DESIGNED to be read as a complete index first (that's
// what makes random access into a zip possible at all, unlike tar's
// sequential-only format). This means the loop below ranges over a
// known-length slice rather than looping until io.EOF, but the
// externally-observable progress-callback CONTRACT (throttled during,
// always exactly one final=true call reflecting the true total) is
// preserved exactly.
func extractZip(archivePath, destDir string, onProgress func(count int64, final bool)) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("not a valid zip archive: %w", err)
	}
	defer r.Close()

	var lastTick time.Time
	for i, entry := range r.File {
		if err := extractZipEntry(entry, destDir); err != nil {
			return err
		}

		count := int64(i + 1)
		final := i == len(r.File)-1
		if onProgress != nil && (final || time.Since(lastTick) >= progressUpdateInterval) {
			onProgress(count, final)
			lastTick = time.Now()
		}
	}

	// An empty archive (zero entries) never enters the loop above, so
	// onProgress would otherwise never receive its guaranteed final
	// call at all -- matching extractTarGz's own behavior, which DOES
	// still call onProgress(0, true) when tr.Next() hits io.EOF
	// immediately on an empty archive.
	if len(r.File) == 0 && onProgress != nil {
		onProgress(0, true)
	}

	return nil
}

// extractZipEntry writes one entry (file, directory, or symlink) to
// its resolved location under destDir. Split out from extractZip's
// own loop so the Zip-Slip guard and per-entry-type handling can be
// tested and reasoned about independently of the progress-throttling
// logic wrapped around it.
func extractZipEntry(entry *zip.File, destDir string) error {
	target := filepath.Join(destDir, entry.Name)
	if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), filepath.Clean(destDir)+string(os.PathSeparator)) {
		return fmt.Errorf("archive entry %q escapes destination directory", entry.Name)
	}

	mode := entry.Mode()

	// See extractTarGz's own comment on POSIX permission bits being a
	// harmless no-op on Windows (NTFS uses ACLs instead) -- the same
	// mode.Perm()/0o755 calls below have identical, already-handled
	// behavior there, so that reasoning isn't repeated a second time
	// here.
	switch {
	case mode&os.ModeSymlink != 0:
		// A zip entry can carry a symlink, but only via Unix-specific
		// external file attributes some zip writers set (confirmed
		// via Go's own archive/zip source: FileInfo().Mode() decodes
		// this from the same field real `zip` CLI implementations on
		// Unix populate) -- genuinely rare for a Windows-targeted JDK
		// archive specifically, but handled for correctness rather
		// than assumed away, the same defense-in-depth spirit as the
		// path-traversal guard above.
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		linkTarget, err := readZipSymlinkTarget(entry)
		if err != nil {
			return err
		}
		if err := os.Symlink(linkTarget, target); err != nil {
			return err
		}
	case mode.IsDir():
		if err := os.MkdirAll(target, dirPerm(mode)); err != nil {
			return err
		}
	default:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		src, err := entry.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, filePerm(mode))
		if err != nil {
			src.Close()
			return err
		}
		_, copyErr := io.Copy(out, src)
		srcCloseErr := src.Close()
		outCloseErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if srcCloseErr != nil {
			return srcCloseErr
		}
		if outCloseErr != nil {
			return outCloseErr
		}
	}

	return nil
}

// dirPerm and filePerm fall back to sensible, standard Unix defaults
// (0o755 for directories, 0o644 for files) whenever a zip entry's own
// decoded mode is missing bits the OWNER genuinely needs to use the
// extracted entry at all.
//
// A real, live-reported bug this fixes, confirmed and precisely
// diagnosed by directly inspecting Go's own archive/zip output (not
// assumed): entries created via the plain Writer.Create convenience
// method (as opposed to CreateHeader+SetMode) decode back as mode
// 0o666 (rw-rw-rw-) -- readable and writable, but with NO execute bit
// for anyone at all, confirmed directly by printing FileHeader.Mode()
// for a real, in-memory test archive. For a FILE, 0o666 is a
// perfectly usable (if permissive) mode. For a DIRECTORY, missing the
// owner's execute bit makes it completely un-enterable, by anyone,
// including its own owner -- Unix requires the execute bit
// specifically to traverse INTO a directory, independent of the read
// bit. This is exactly why creating "bin" inside an already-extracted
// parent directory failed with "permission denied": the PARENT
// itself had been created with this same 0o666 mode, making it
// impossible to create anything inside it at all.
//
// The original version of this fix checked for mode.Perm() == 0
// specifically -- diagnosed from reasoning about Go's source alone,
// without actually running it. That check never fires for the REAL,
// empirically-confirmed 0o666 value, so it silently failed to fix
// anything -- caught immediately by
// TestExtractZip_ZeroModeEntriesFallBackToSensibleDefaults actually
// asserting on the resulting mode bits, rather than merely on
// whether extraction returned an error.
//
// dirPerm specifically requires ALL of owner-read+write+execute
// (0o700) to be present before trusting the archive's own mode;
// filePerm only requires the entry to carry ANY permission bits at
// all (files don't need an execute bit to be usable, so 0o666 is
// left alone, unlike for directories). Genuine vendor archives built
// by real zip tools are expected to set these correctly -- this
// fallback is defense-in-depth for archives that don't (confirmed:
// specifically a gap in Go's OWN bare Create() convenience method,
// not a general zip-format limitation), not an assumption that real
// vendor archives need it.
func dirPerm(mode os.FileMode) os.FileMode {
	if mode.Perm()&0o700 == 0o700 {
		return mode.Perm()
	}
	return 0o755
}

func filePerm(mode os.FileMode) os.FileMode {
	if p := mode.Perm(); p != 0 {
		return p
	}
	return 0o644
}

// readZipSymlinkTarget reads a symlink entry's target path -- for a
// symlink, archive/zip stores the target STRING as the entry's own
// "file content" (confirmed via Go's own archive/zip documentation
// and the same convention Info-ZIP and Unix `zip`/`unzip` use),
// rather than as file metadata the way tar.Header.Linkname is.
func readZipSymlinkTarget(entry *zip.File) (string, error) {
	src, err := entry.Open()
	if err != nil {
		return "", err
	}
	defer src.Close()
	data, err := io.ReadAll(src)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

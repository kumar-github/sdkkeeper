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

// extractZip extracts a .zip archive into destDir -- the format both
// Temurin and Liberica publish their Windows JDK builds as, vs.
// .tar.gz on darwin/linux. Mirrors extractTarGz's signature, progress
// contract, and Zip-Slip guard. archive/zip's Reader gives the full
// entry list up front (r.File), unlike tar's streaming, one-entry-
// at-a-time API, so this loop ranges over a known-length slice rather
// than looping until io.EOF, but the progress-callback contract
// (throttled during, one final=true call) is preserved exactly.
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

	// An empty archive never enters the loop above, so this ensures
	// onProgress still gets its guaranteed final call, matching
	// extractTarGz's behavior for an empty archive.
	if len(r.File) == 0 && onProgress != nil {
		onProgress(0, true)
	}

	return nil
}

// extractZipEntry writes one entry (file, directory, or symlink) to
// its resolved location under destDir, independent of the progress-
// throttling logic wrapped around it in extractZip.
func extractZipEntry(entry *zip.File, destDir string) error {
	target := filepath.Join(destDir, entry.Name)
	if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), filepath.Clean(destDir)+string(os.PathSeparator)) {
		return fmt.Errorf("archive entry %q escapes destination directory", entry.Name)
	}

	mode := entry.Mode()

	// POSIX permission bits are a harmless no-op on Windows (NTFS
	// uses ACLs instead) -- see extractTarGz.
	switch {
	case mode&os.ModeSymlink != 0:
		// A zip entry can carry a symlink via Unix-specific external
		// file attributes some zip writers set -- rare for a
		// Windows-targeted archive, but handled for correctness.
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

// dirPerm and filePerm fall back to sensible Unix defaults (0o755 for
// directories, 0o644 for files) when a zip entry's decoded mode is
// missing bits the owner needs to use it. Entries created via Go's
// plain Writer.Create decode back as mode 0o666 (rw-rw-rw-, no
// execute bit) -- fine for a file, but a directory missing the
// owner's execute bit is completely un-enterable, which is why
// creating "bin" inside an extracted parent could fail with
// "permission denied". dirPerm requires all of owner-rwx (0o700)
// before trusting the archive's mode; filePerm only requires any
// permission bits at all, since files don't need an execute bit.
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

// readZipSymlinkTarget reads a symlink entry's target path --
// archive/zip stores it as the entry's own file content, unlike
// tar.Header.Linkname's metadata field.
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

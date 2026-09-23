package cli

import (
	"io/fs"
	"path/filepath"
)

// dirSize sums the real, on-disk size of every regular file under
// path. There's no OS-level shortcut for this -- an ordinary
// directory has no precomputed total anywhere (du, PowerShell's
// Measure-Object, and this WalkDir all do the identical underlying
// work of visiting every file and summing, just in different
// languages); the only exception is a directory that's its own
// filesystem/subvolume with quota tracking, which a plain candidate
// folder under ~/.sdkkeeper/candidates/... never is.
//
// Errors partway through (a permission-denied subdirectory, a file
// that vanishes mid-walk) are silently skipped rather than aborting
// the whole walk -- `sk list --sizes` is a best-effort convenience
// report, not a validation, and one unreadable entry shouldn't blank
// out an otherwise-computable total.
func dirSize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}

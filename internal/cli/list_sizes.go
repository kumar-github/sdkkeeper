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
// Resolves path itself if it's a symlink (an `add`-registered entry
// always is) BEFORE walking -- filepath.WalkDir does not follow a
// symlink that IS the root it's given; it reports that root as a
// single non-directory leaf and returns the symlink's OWN lstat size
// (the byte-length of the target path string, e.g. ~25-60 bytes),
// never descending into what it points to at all. Confirmed directly
// (not just reasoned about): walking a 50 MB real directory returns
// 52428800 as expected, but walking a symlink TO that same directory
// returned 25 -- the length of "/tmp/symlinktest/real-jdk". Every
// symlink encountered WHILE walking (a file inside the tree, not the
// root itself) is left exactly as WalkDir already treats it by
// default -- unfollowed, its own small lstat size counted -- since
// that one either doesn't arise in a real JDK/SDK layout or is a
// separate, much narrower case than the root-is-a-symlink bug this
// resolves.
//
// Errors partway through (a permission-denied subdirectory, a file
// that vanishes mid-walk, a symlink that can't be resolved at all)
// are silently skipped rather than aborting the whole walk -- `sk
// list --sizes` is a best-effort convenience report, not a
// validation, and one unreadable entry shouldn't blank out an
// otherwise-computable total.
func dirSize(path string) int64 {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
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

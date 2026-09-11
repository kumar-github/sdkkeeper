package installer

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// buildZip mirrors buildTarGz exactly (same signature, same
// topDir-plus-files shape, same checksum-returning contract) -- a
// deliberate structural match so a reader already familiar with the
// tar.gz test helper immediately understands this one.
//
// Deliberately uses CreateHeader+SetMode, NOT the plain Create()
// convenience method -- a real, live-reported bug found via this
// exact distinction: Create() never sets CreatorVersion at all, so
// FileHeader.Mode() decodes back to ZERO permission bits for every
// entry it creates (confirmed directly from Go's own archive/zip
// source). This helper existing WITH that bug is exactly what let a
// real production bug in extractZipEntry go unnoticed by every test
// using it, purely because this sandbox's own tests run as root
// (which bypasses permission-bit enforcement entirely) -- a real,
// non-root user hit "permission denied" immediately. Setting
// realistic modes here matches how genuine vendor archives are
// actually built (by real zip tools, which DO set these bits
// correctly), so this helper now exercises the same shape of archive
// extractZip will actually see in production, not an artificially
// permission-less one.
func buildZip(t *testing.T, topDir string, files map[string]string) ([]byte, string) {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	dirHeader := &zip.FileHeader{Name: topDir + "/"}
	dirHeader.SetMode(0o755 | os.ModeDir)
	if _, err := zw.CreateHeader(dirHeader); err != nil {
		t.Fatalf("writing top-level dir entry: %v", err)
	}

	for name, content := range files {
		full := topDir + "/" + name
		fileHeader := &zip.FileHeader{Name: full, Method: zip.Deflate}
		fileHeader.SetMode(0o644)
		w, err := zw.CreateHeader(fileHeader)
		if err != nil {
			t.Fatalf("writing header for %s: %v", full, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("writing content for %s: %v", full, err)
		}
	}

	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip writer: %v", err)
	}
	data := buf.Bytes()
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:])
}

// TestExtractZip_ProgressReportsFinalCount mirrors
// TestExtractTarGz_ProgressReportsFinalCount exactly -- same
// assertion shape (exactly one final=true call, matching the real
// entry count), confirming both formats give install.go's CLI wiring
// the identical guarantee regardless of which vendor/OS combination
// resolved to which archive type.
func TestExtractZip_ProgressReportsFinalCount(t *testing.T) {
	files := map[string]string{
		"bin/java.exe": "a", "bin/javac.exe": "b", "lib/foo.dll": "c",
	}
	archive, _ := buildZip(t, "jdk-21.0.2", files)

	tmpRoot := t.TempDir()
	archivePath := filepath.Join(tmpRoot, "archive.zip")
	os.WriteFile(archivePath, archive, 0o644)
	destDir := filepath.Join(tmpRoot, "dest")
	os.MkdirAll(destDir, 0o755)

	var finalCalls int
	var lastCount int64
	err := extractZip(archivePath, destDir, func(count int64, final bool) {
		lastCount = count
		if final {
			finalCalls++
		}
	})
	if err != nil {
		t.Fatalf("extractZip failed: %v", err)
	}
	if finalCalls != 1 {
		t.Errorf("expected exactly 1 final=true call, got %d", finalCalls)
	}
	// 3 files + the top-level directory entry itself = 4 total entries.
	if lastCount != 4 {
		t.Errorf("expected final count to be 4 (3 files + top-level dir), got %d", lastCount)
	}
}

// TestExtractZip_ProgressNilIsSafe mirrors
// TestExtractTarGz_ProgressNilIsSafe.
func TestExtractZip_ProgressNilIsSafe(t *testing.T) {
	archive, _ := buildZip(t, "jdk-21.0.2", map[string]string{"bin/java.exe": "x"})

	tmpRoot := t.TempDir()
	archivePath := filepath.Join(tmpRoot, "archive.zip")
	os.WriteFile(archivePath, archive, 0o644)
	destDir := filepath.Join(tmpRoot, "dest")
	os.MkdirAll(destDir, 0o755)

	if err := extractZip(archivePath, destDir, nil); err != nil {
		t.Fatalf("extractZip failed: %v", err)
	}
}

// TestExtractZip_RejectsPathTraversal mirrors
// TestExtractTarGz_RejectsPathTraversal -- same Zip-Slip attack shape
// (an entry named to escape destDir via "../../"), confirming the
// guard extractZipEntry shares the same defense-in-depth reasoning
// with, works identically for the zip format too.
func TestExtractZip_RejectsPathTraversal(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("../../etc/passwd")
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	w.Write([]byte("evil"))
	zw.Close()

	tmpRoot := t.TempDir()
	archivePath := filepath.Join(tmpRoot, "malicious.zip")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	destDir := filepath.Join(tmpRoot, "dest")
	os.MkdirAll(destDir, 0o755)

	err = extractZip(archivePath, destDir, nil)
	if err == nil {
		t.Fatal("expected path traversal to be rejected")
	}
}

// TestExtractZip_EmptyArchiveStillReportsFinal is new coverage (no
// tar.gz equivalent exists -- tar's streaming Next()/io.EOF loop
// naturally reaches its "final" call even for zero entries, but
// zip's own slice-based loop needed an explicit post-loop check for
// this case, added specifically in extractZip -- see its own comment).
// Confirms that check actually works, not just that it compiles.
func TestExtractZip_EmptyArchiveStillReportsFinal(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	zw.Close() // zero entries

	tmpRoot := t.TempDir()
	archivePath := filepath.Join(tmpRoot, "empty.zip")
	os.WriteFile(archivePath, buf.Bytes(), 0o644)
	destDir := filepath.Join(tmpRoot, "dest")
	os.MkdirAll(destDir, 0o755)

	var finalCalls int
	err := extractZip(archivePath, destDir, func(count int64, final bool) {
		if final {
			finalCalls++
		}
	})
	if err != nil {
		t.Fatalf("extractZip failed: %v", err)
	}
	if finalCalls != 1 {
		t.Errorf("expected exactly 1 final=true call even for an empty archive, got %d", finalCalls)
	}
}

// TestExtractZip_ExtractsSymlink is new coverage closing a real,
// pre-existing gap: neither extractTarGz nor extractZip had ANY test
// exercising their symlink-handling branch before this, despite both
// implementations having real, non-trivial code for it. Uses a real
// zip file with Unix symlink external attributes set directly (Go's
// archive/zip has no high-level "add a symlink" helper -- the mode
// bits must be set on the FileHeader manually, mirroring exactly what
// a real Unix `zip` CLI would encode).
// TestExtractZip_ZeroModeEntriesFallBackToSensibleDefaults is a
// regression test for a real, live-reported bug: a zip entry with NO
// Unix permission bits encoded at all (mode.Perm() == 0 -- exactly
// what Go's own archive/zip Writer.Create produces, see buildZip's
// own comment) used to be extracted with that same, literal
// zero-permission mode -- a directory created with chmod 000, no
// access for even its own owner. This went unnoticed in this
// project's own sandbox purely because sandbox tests run as root
// (root bypasses permission-bit enforcement entirely), but failed
// immediately on a real, non-root user account with "permission
// denied" trying to extract anything into that directory.
//
// Deliberately builds the archive with the PLAIN Create() method
// (not buildZip's now-realistic CreateHeader+SetMode), specifically
// to reproduce a zero-mode entry -- and asserts on the ACTUAL,
// resulting os.Stat mode bits on disk, not merely that extraction
// returned no error. That distinction matters: this project's own
// sandbox runs as root, where the ORIGINAL bug's extraction call
// would have "succeeded" too (root can write into a chmod-000
// directory) -- only a direct assertion on the stored permission
// value itself would have caught this regardless of which user ran
// the test, which is exactly why the bug reached a real user
// undetected in the first place.
func TestExtractZip_ZeroModeEntriesFallBackToSensibleDefaults(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if _, err := zw.Create("jdk-21.0.2/bin/"); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	w, err := zw.Create("jdk-21.0.2/bin/java.exe")
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	w.Write([]byte("fake binary"))
	zw.Close()

	tmpRoot := t.TempDir()
	archivePath := filepath.Join(tmpRoot, "archive.zip")
	os.WriteFile(archivePath, buf.Bytes(), 0o644)
	destDir := filepath.Join(tmpRoot, "dest")
	os.MkdirAll(destDir, 0o755)

	if err := extractZip(archivePath, destDir, nil); err != nil {
		t.Fatalf("extractZip failed: %v", err)
	}

	dirInfo, err := os.Stat(filepath.Join(destDir, "jdk-21.0.2", "bin"))
	if err != nil {
		t.Fatalf("expected the directory to exist: %v", err)
	}
	// Checked via the OWNER's execute bit specifically, not an exact
	// numeric mode -- a real gap caught via actual testing as a
	// non-root user: the umask that applies when MkdirAll/OpenFile
	// actually create something on disk varies legitimately by
	// environment (root's default umask and a regular user's default
	// commonly differ, e.g. 0022 vs 0002), so asserting an exact
	// post-umask value is inherently environment-fragile. What
	// actually matters -- and is genuinely NOT umask-dependent, since
	// umask only ever CLEARS bits, never sets ones dirPerm didn't
	// already request -- is that the owner's execute bit made it
	// through, since THAT's the specific bit the original bug was
	// missing entirely.
	if dirInfo.Mode().Perm()&0o100 == 0 {
		t.Errorf("directory is missing the owner's execute bit (mode %o) -- the exact bug this test regresses against: a directory nobody, not even its own owner, can enter", dirInfo.Mode().Perm())
	}

	fileInfo, err := os.Stat(filepath.Join(destDir, "jdk-21.0.2", "bin", "java.exe"))
	if err != nil {
		t.Fatalf("expected the file to exist: %v", err)
	}
	if fileInfo.Mode().Perm()&0o400 == 0 {
		t.Errorf("file is missing the owner's read bit (mode %o) -- effectively inaccessible to its own owner", fileInfo.Mode().Perm())
	}
}

// TestDirPerm and TestFilePerm test the fallback logic directly and
// in isolation, confirming a REAL, non-zero mode from a well-formed
// archive is correctly PRESERVED, not overridden -- the fallback
// must be narrowly scoped to an unusable mode specifically, never a
// blanket override of whatever a legitimate archive specifies.
func TestDirPerm(t *testing.T) {
	// 0o666 is the REAL, empirically-confirmed mode Go's own bare
	// zip.Writer.Create produces (see dirPerm's own doc comment) --
	// tested explicitly, not just the more extreme, less realistic
	// "0" case, since that's the actual value that let the original
	// bug slip past a check for == 0 specifically.
	if got := dirPerm(os.FileMode(0o666)); got != 0o755 {
		t.Errorf("dirPerm(0o666) = %o, want 0o755 (fallback -- missing the owner's execute bit)", got)
	}
	if got := dirPerm(os.FileMode(0)); got != 0o755 {
		t.Errorf("dirPerm(0) = %o, want 0o755 (fallback)", got)
	}
	if got := dirPerm(os.ModeDir | 0o700); got != 0o700 {
		t.Errorf("dirPerm(0o700) = %o, want 0o700 (a real, legitimate mode must be preserved, not overridden)", got)
	}
	if got := dirPerm(os.ModeDir | 0o750); got != 0o750 {
		t.Errorf("dirPerm(0o750) = %o, want 0o750 (owner has full rwx -- must be preserved even though group/other differ)", got)
	}
}

func TestFilePerm(t *testing.T) {
	if got := filePerm(os.FileMode(0)); got != 0o644 {
		t.Errorf("filePerm(0) = %o, want 0o644 (fallback)", got)
	}
	if got := filePerm(os.FileMode(0o666)); got != 0o666 {
		t.Errorf("filePerm(0o666) = %o, want 0o666 (a real mode, even a permissive one, must be preserved for files -- unlike directories, files don't need an execute bit to be usable)", got)
	}
	if got := filePerm(os.FileMode(0o600)); got != 0o600 {
		t.Errorf("filePerm(0o600) = %o, want 0o600 (a real, legitimate mode must be preserved, not overridden)", got)
	}
}

func TestExtractZip_ExtractsSymlink(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	fh := &zip.FileHeader{Name: "jdk-21.0.2/bin/java"}
	fh.SetMode(os.ModeSymlink | 0o777)
	w, err := zw.CreateHeader(fh)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	// A symlink entry's own "content" IS the link target string --
	// see readZipSymlinkTarget's doc comment for why.
	w.Write([]byte("../lib/jvm/actual-java-binary"))
	zw.Close()

	tmpRoot := t.TempDir()
	archivePath := filepath.Join(tmpRoot, "archive.zip")
	os.WriteFile(archivePath, buf.Bytes(), 0o644)
	destDir := filepath.Join(tmpRoot, "dest")
	os.MkdirAll(destDir, 0o755)

	if err := extractZip(archivePath, destDir, nil); err != nil {
		t.Fatalf("extractZip failed: %v", err)
	}

	linkPath := filepath.Join(destDir, "jdk-21.0.2", "bin", "java")
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("expected a symlink at %s, got error: %v", linkPath, err)
	}
	// See TestExtractTarGz_ExtractsSymlink's own comment -- the same
	// real, live-reported Windows separator-normalization finding
	// applies identically here.
	if filepath.ToSlash(target) != "../lib/jvm/actual-java-binary" {
		t.Errorf("expected symlink target %q, got %q", "../lib/jvm/actual-java-binary", target)
	}
}

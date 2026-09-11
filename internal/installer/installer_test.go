package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// buildTarGz creates a real, valid .tar.gz in memory containing a
// single top-level directory (topDir) with the given files inside it
// (relative paths, mapped to their content) -- matching the real shape
// of a JDK archive (one wrapper directory, e.g. "jdk-21.0.2+13/...").
// Returns the archive bytes and its correct SHA256 checksum.
func buildTarGz(t *testing.T, topDir string, files map[string]string) ([]byte, string) {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	if err := tw.WriteHeader(&tar.Header{
		Name:     topDir + "/",
		Typeflag: tar.TypeDir,
		Mode:     0o755,
	}); err != nil {
		t.Fatalf("writing top-level dir header: %v", err)
	}

	for name, content := range files {
		full := topDir + "/" + name
		if err := tw.WriteHeader(&tar.Header{
			Name:     full,
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Size:     int64(len(content)),
		}); err != nil {
			t.Fatalf("writing header for %s: %v", full, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("writing content for %s: %v", full, err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("closing gzip writer: %v", err)
	}

	data := buf.Bytes()
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:])
}

func TestNewHasher_SelectsCorrectAlgorithm(t *testing.T) {
	cases := map[string]string{
		"sha256":      "sha256",
		"sha1":        "sha1",
		"sha512":      "sha512",
		"":            "sha256", // unrecognized/empty defaults to sha256
		"unsupported": "sha256", // truly unknown also defaults to sha256
	}
	for algorithm, wantFamily := range cases {
		h := newHasher(algorithm)
		var gotSize int
		switch wantFamily {
		case "sha256":
			gotSize = sha256.Size
		case "sha1":
			gotSize = sha1.Size
		case "sha512":
			gotSize = sha512.Size
		}
		if h.Size() != gotSize {
			t.Errorf("newHasher(%q): expected digest size %d (%s), got %d", algorithm, gotSize, wantFamily, h.Size())
		}
	}
}

func TestInstall_Success(t *testing.T) {
	archive, checksum := buildTarGz(t, "jdk-21.0.2+13", map[string]string{
		"bin/java":              "fake java binary",
		"Contents/Home/bin/foo": "nested content",
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer server.Close()

	tmpRoot := t.TempDir()
	targetDir := filepath.Join(tmpRoot, "candidates", "java", "JDK-21.0.2-temurin")
	tempRoot := filepath.Join(tmpRoot, "tmp")

	var downloadProgressCalls int
	err := Install(context.Background(), Options{
		URL:              server.URL,
		Filename:         "archive.tar.gz",
		Checksum:         checksum,
		TargetDir:        targetDir,
		TempRoot:         tempRoot,
		DownloadProgress: func(read, total int64, final bool) { downloadProgressCalls++ },
	})
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(targetDir, "bin", "java"))
	if err != nil {
		t.Fatalf("expected extracted file to exist: %v", err)
	}
	if string(content) != "fake java binary" {
		t.Errorf("unexpected content: %s", content)
	}

	// DownloadProgress is the CURRENT, correct mechanism for
	// confirming progress feedback reaches the caller during a normal
	// install -- an earlier version of this test checked Progress
	// instead, which was valid when "Downloading..."/"Extracting..."
	// printed unconditionally, but both are now conditional (see
	// TestInstall_FastExtractionStaysSilent /
	// TestInstall_SlowExtractionShowsMessage for that specific
	// behavior) -- a fast, successful install correctly produces ZERO
	// Progress messages now, which is the intended behavior, not a
	// gap.
	if downloadProgressCalls == 0 {
		t.Error("expected at least one DownloadProgress call")
	}

	// Confirm TempRoot itself is gone entirely, not just emptied --
	// a real question raised after actual use: an empty leftover
	// directory at ~/.sdkkeeper/tmp, while harmless, looks like
	// unexplained cruft. os.Stat (not ReadDir) specifically confirms
	// the DIRECTORY ITSELF no longer exists, not merely that it has
	// no contents.
	if _, err := os.Stat(tempRoot); !os.IsNotExist(err) {
		t.Errorf("expected TempRoot itself to be removed after success, but Stat returned: %v", err)
	}
}

// TestRunWithThresholdedProgress_FastWorkStaysSilent confirms onSlow
// is NOT called when work finishes well before the threshold -- the
// common case (e.g. a normal, sub-second extraction), where a status
// message would otherwise just flash by unread.
func TestRunWithThresholdedProgress_FastWorkStaysSilent(t *testing.T) {
	var called bool
	err := runWithThresholdedProgress(
		50*time.Millisecond,
		func() { called = true },
		func() error { return nil }, // instant
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Error("expected onSlow NOT to be called for fast work")
	}
}

// TestRunWithThresholdedProgress_SlowWorkCallsOnSlow confirms onSlow
// IS called when work genuinely takes longer than the threshold --
// using a real time.Sleep for deterministic timing, completely
// decoupled from actual filesystem/extraction speed (an earlier
// version of this test tried to force this via a near-zero threshold
// against a real, tiny archive extraction, and failed consistently: a
// newly-spawned goroutine isn't scheduled instantly, so for a
// near-instant operation, the main goroutine can finish and signal
// completion before the timer goroutine even gets scheduled to check
// its own select, regardless of how tiny the nominal threshold is).
func TestRunWithThresholdedProgress_SlowWorkCallsOnSlow(t *testing.T) {
	var called bool
	err := runWithThresholdedProgress(
		10*time.Millisecond,
		func() { called = true },
		func() error {
			time.Sleep(50 * time.Millisecond) // deliberately, reliably slower than the threshold
			return nil
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected onSlow to be called for slow work")
	}
}

// TestRunWithThresholdedProgress_PropagatesError confirms work's error
// is still returned correctly regardless of which side of the
// threshold it finished on.
func TestRunWithThresholdedProgress_PropagatesError(t *testing.T) {
	wantErr := fmt.Errorf("boom")
	err := runWithThresholdedProgress(time.Hour, nil, func() error { return wantErr })
	if err != wantErr {
		t.Errorf("expected the underlying error to be returned, got: %v", err)
	}
}

// TestInstall_FastExtractionSuppressesExtractionProgress is a
// regression test for a real bug caught before shipping: the gating
// condition was originally "final || crossedThreshold.Load()", which
// meant a fast extraction that never crossed the threshold would
// still forward its own final call -- one line still appearing at the
// very end, defeating the whole point of staying silent for the
// common, fast case. Fixed to just "crossedThreshold.Load()"; this
// confirms a fast extraction now produces ZERO ExtractionProgress
// calls to the caller, including the final one.
// TestInstall_Success_ZipFormat mirrors TestInstall_Success exactly,
// confirming the FULL Install pipeline -- download, checksum
// verification, extractor dispatch, placement -- works correctly
// end-to-end for a .zip-suffixed Filename, not just extractZip in
// isolation. This is the test that actually proves the dispatch wired
// into Install reaches extractZip correctly for a real install, which
// none of extractZip's own direct unit tests (above) could confirm on
// their own.
func TestInstall_Success_ZipFormat(t *testing.T) {
	archive, checksum := buildZip(t, "jdk-21.0.2+13", map[string]string{
		"bin/java.exe":          "fake java binary",
		"Contents/Home/bin/foo": "nested content",
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer server.Close()

	tmpRoot := t.TempDir()
	targetDir := filepath.Join(tmpRoot, "candidates", "java", "JDK-21.0.2-temurin")
	tempRoot := filepath.Join(tmpRoot, "tmp")

	err := Install(context.Background(), Options{
		URL:       server.URL,
		Filename:  "OpenJDK21U-jdk_x64_windows_hotspot_21.0.2_13.zip",
		Checksum:  checksum,
		TargetDir: targetDir,
		TempRoot:  tempRoot,
	})
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(targetDir, "bin", "java.exe"))
	if err != nil {
		t.Fatalf("expected extracted file to exist: %v", err)
	}
	if string(content) != "fake java binary" {
		t.Errorf("unexpected content: %s", content)
	}
}

func TestInstall_FastExtractionSuppressesExtractionProgress(t *testing.T) {
	archive, checksum := buildTarGz(t, "jdk-21.0.2+13", map[string]string{"bin/java": "fake"})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer server.Close()

	tmpRoot := t.TempDir()
	var calls int

	err := Install(context.Background(), Options{
		URL:                         server.URL,
		Filename:                    "archive.tar.gz",
		Checksum:                    checksum,
		TargetDir:                   filepath.Join(tmpRoot, "candidates", "java", "JDK-21.0.2"),
		TempRoot:                    filepath.Join(tmpRoot, "tmp"),
		ExtractionProgressThreshold: time.Hour, // never crosses for a fast, tiny extraction
		ExtractionProgress:          func(count int64, final bool) { calls++ },
	})
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}
	if calls != 0 {
		t.Errorf("expected ZERO ExtractionProgress calls for a fast extraction (including the final one), got %d", calls)
	}
}

func TestInstall_AlreadyExists(t *testing.T) {
	tmpRoot := t.TempDir()
	targetDir := filepath.Join(tmpRoot, "candidates", "java", "JDK-21.0.2-temurin")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	requestMade := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestMade = true
	}))
	defer server.Close()

	err := Install(context.Background(), Options{
		URL:       server.URL,
		Filename:  "archive.tar.gz",
		Checksum:  "irrelevant",
		TargetDir: targetDir,
		TempRoot:  filepath.Join(tmpRoot, "tmp"),
	})
	if err != ErrAlreadyExists {
		t.Errorf("expected ErrAlreadyExists, got: %v", err)
	}
	if requestMade {
		t.Error("expected NO network request to be made when target already exists")
	}
}

func TestInstall_ChecksumMismatch_RetriesThenSucceeds(t *testing.T) {
	goodArchive, goodChecksum := buildTarGz(t, "jdk-21.0.2+13", map[string]string{"bin/java": "real content"})
	badArchive := []byte("this is not the right content at all")

	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt < 3 {
			w.Write(badArchive)
			return
		}
		w.Write(goodArchive)
	}))
	defer server.Close()

	tmpRoot := t.TempDir()
	targetDir := filepath.Join(tmpRoot, "candidates", "java", "JDK-21.0.2-temurin")

	err := Install(context.Background(), Options{
		URL:         server.URL,
		Filename:    "archive.tar.gz",
		Checksum:    goodChecksum,
		TargetDir:   targetDir,
		TempRoot:    filepath.Join(tmpRoot, "tmp"),
		MaxAttempts: 3,
		RetryDelay:  10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("expected success on the 3rd attempt, got error: %v", err)
	}
	if attempt != 3 {
		t.Errorf("expected exactly 3 attempts, got %d", attempt)
	}
}

func TestInstall_ChecksumMismatch_ExhaustsRetries(t *testing.T) {
	badArchive := []byte("never the right content")

	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		w.Write(badArchive)
	}))
	defer server.Close()

	tmpRoot := t.TempDir()
	targetDir := filepath.Join(tmpRoot, "candidates", "java", "JDK-21.0.2-temurin")
	tempRoot := filepath.Join(tmpRoot, "tmp")

	err := Install(context.Background(), Options{
		URL:         server.URL,
		Filename:    "archive.tar.gz",
		Checksum:    "0000000000000000000000000000000000000000000000000000000000000",
		TargetDir:   targetDir,
		TempRoot:    tempRoot,
		MaxAttempts: 3,
		RetryDelay:  10 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected an error after exhausting all retries")
	}
	if attempt != 3 {
		t.Errorf("expected exactly 3 attempts, got %d", attempt)
	}

	if _, statErr := os.Stat(targetDir); statErr == nil {
		t.Error("TargetDir should NOT exist after a failed install")
	}

	// Confirm TempRoot itself is gone entirely after exhausting
	// retries too, not just emptied -- directly the failure mode
	// researched in both Homebrew's and SDKMAN's real GitHub issues
	// (stale cached/partial downloads confusing later attempts), now
	// also proven to leave literally nothing behind, not even an
	// empty directory.
	if _, err := os.Stat(tempRoot); !os.IsNotExist(err) {
		t.Errorf("expected TempRoot itself to be removed after exhausting retries, but Stat returned: %v", err)
	}
}

func TestInstall_NetworkFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	tmpRoot := t.TempDir()
	err := Install(context.Background(), Options{
		URL:         server.URL,
		Filename:    "archive.tar.gz",
		Checksum:    "irrelevant",
		TargetDir:   filepath.Join(tmpRoot, "candidates", "java", "JDK-21.0.2"),
		TempRoot:    filepath.Join(tmpRoot, "tmp"),
		MaxAttempts: 2,
		RetryDelay:  10 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected an error for a server that always returns 500")
	}
}

// TestExtractTarGz_ProgressReportsFinalCount confirms onProgress is
// called with final=true exactly once, and the final count matches the
// real number of entries in the archive -- the guarantee install.go's
// CLI wiring depends on to know definitively when extraction is done.
func TestExtractorFor_TarGz(t *testing.T) {
	ext, err := extractorFor("OpenJDK21U-jdk_x64_linux_hotspot_21.0.2_13.tar.gz")
	if err != nil {
		t.Fatalf("expected .tar.gz to resolve, got error: %v", err)
	}
	if ext == nil {
		t.Fatal("expected a non-nil extractor for .tar.gz")
	}
}

func TestExtractorFor_Zip(t *testing.T) {
	ext, err := extractorFor("OpenJDK21U-jdk_x64_windows_hotspot_21.0.2_13.zip")
	if err != nil {
		t.Fatalf("expected .zip to resolve, got error: %v", err)
	}
	if ext == nil {
		t.Fatal("expected a non-nil extractor for .zip")
	}
}

// TestExtractorFor_UnrecognizedFormat confirms a clear, specific error
// naming the actual unrecognized filename -- not a generic failure --
// for anything that isn't .tar.gz or .zip, including the empty string
// a caller that forgot to set Options.Filename would produce.
func TestExtractorFor_UnrecognizedFormat(t *testing.T) {
	for _, filename := range []string{"", "archive.rar", "archive.tar", "archive"} {
		_, err := extractorFor(filename)
		if err == nil {
			t.Errorf("expected an error for unrecognized filename %q", filename)
		}
	}
}

// TestInstall_UnrecognizedFormatFailsBeforeNetworkActivity confirms
// the dispatch validation genuinely happens BEFORE any download is
// attempted -- an httptest.Server that would fail the test if it ever
// received a request proves this directly, rather than just checking
// the error message's wording.
func TestInstall_UnrecognizedFormatFailsBeforeNetworkActivity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should NEVER be contacted -- Filename validation must happen first")
	}))
	defer server.Close()

	tmpRoot := t.TempDir()
	err := Install(context.Background(), Options{
		URL:       server.URL,
		Filename:  "archive.rar",
		Checksum:  "irrelevant",
		TargetDir: filepath.Join(tmpRoot, "candidates", "java", "JDK-21.0.2"),
		TempRoot:  filepath.Join(tmpRoot, "tmp"),
	})
	if err == nil {
		t.Fatal("expected an error for an unrecognized archive format")
	}
}

func TestExtractTarGz_ProgressReportsFinalCount(t *testing.T) {
	files := map[string]string{
		"bin/java": "a", "bin/javac": "b", "lib/foo.so": "c",
	}
	archive, _ := buildTarGz(t, "jdk-21.0.2", files)

	tmpRoot := t.TempDir()
	archivePath := filepath.Join(tmpRoot, "archive.tar.gz")
	os.WriteFile(archivePath, archive, 0o644)
	destDir := filepath.Join(tmpRoot, "dest")
	os.MkdirAll(destDir, 0o755)

	var finalCalls int
	var lastCount int64
	err := extractTarGz(archivePath, destDir, func(count int64, final bool) {
		lastCount = count
		if final {
			finalCalls++
		}
	})
	if err != nil {
		t.Fatalf("extractTarGz failed: %v", err)
	}
	if finalCalls != 1 {
		t.Errorf("expected exactly 1 final=true call, got %d", finalCalls)
	}
	// 3 files + the top-level directory entry itself = 4 total entries.
	if lastCount != 4 {
		t.Errorf("expected final count to be 4 (3 files + top-level dir), got %d", lastCount)
	}
}

// TestExtractTarGz_ProgressNilIsSafe confirms passing nil for
// onProgress (the common case -- most callers, including tests, don't
// care about extraction progress) doesn't panic.
func TestExtractTarGz_ProgressNilIsSafe(t *testing.T) {
	archive, _ := buildTarGz(t, "jdk-21.0.2", map[string]string{"bin/java": "x"})

	tmpRoot := t.TempDir()
	archivePath := filepath.Join(tmpRoot, "archive.tar.gz")
	os.WriteFile(archivePath, archive, 0o644)
	destDir := filepath.Join(tmpRoot, "dest")
	os.MkdirAll(destDir, 0o755)

	if err := extractTarGz(archivePath, destDir, nil); err != nil {
		t.Fatalf("extractTarGz failed: %v", err)
	}
}

func TestExtractTarGz_RejectsPathTraversal(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{
		Name:     "../../etc/passwd",
		Typeflag: tar.TypeReg,
		Mode:     0o644,
		Size:     4,
	})
	tw.Write([]byte("evil"))
	tw.Close()
	gz.Close()

	tmpRoot := t.TempDir()
	archivePath := filepath.Join(tmpRoot, "malicious.tar.gz")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	destDir := filepath.Join(tmpRoot, "dest")
	os.MkdirAll(destDir, 0o755)

	err := extractTarGz(archivePath, destDir, nil)
	if err == nil {
		t.Fatal("expected path traversal to be rejected")
	}
}

// TestExtractTarGz_ExtractsSymlink is new coverage closing a real,
// pre-existing gap found while strengthening extractZip's own test
// suite: extractTarGz's symlink-handling branch (tar.TypeSymlink) had
// NO test exercising it at all before this, despite being real,
// non-trivial code.
func TestExtractTarGz_ExtractsSymlink(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{
		Name:     "jdk-21.0.2/bin/java",
		Typeflag: tar.TypeSymlink,
		Linkname: "../lib/jvm/actual-java-binary",
		Mode:     0o777,
	})
	tw.Close()
	gz.Close()

	tmpRoot := t.TempDir()
	archivePath := filepath.Join(tmpRoot, "archive.tar.gz")
	os.WriteFile(archivePath, buf.Bytes(), 0o644)
	destDir := filepath.Join(tmpRoot, "dest")
	os.MkdirAll(destDir, 0o755)

	if err := extractTarGz(archivePath, destDir, nil); err != nil {
		t.Fatalf("extractTarGz failed: %v", err)
	}

	linkPath := filepath.Join(destDir, "jdk-21.0.2", "bin", "java")
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("expected a symlink at %s, got error: %v", linkPath, err)
	}
	// filepath.ToSlash before comparing -- a real, live-reported
	// finding from a real Windows CI run: Windows' own symlink
	// target storage/retrieval normalizes the separator style
	// (confirmed directly: the exact same forward-slash target
	// written here reads back with backslashes there), which is
	// expected, correct OS-native behavior, not a functional bug --
	// the symlink still points to the right place either way, since
	// Windows generally accepts "/" as an alternate separator in
	// path resolution too. An exact, un-normalized string comparison
	// was asserting on incidental OS display formatting, not on
	// anything that actually matters functionally.
	if filepath.ToSlash(target) != "../lib/jvm/actual-java-binary" {
		t.Errorf("expected symlink target %q, got %q", "../lib/jvm/actual-java-binary", target)
	}
}

func TestSingleTopLevelDir_RejectsMultipleDirs(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "dir-one"), 0o755)
	os.MkdirAll(filepath.Join(root, "dir-two"), 0o755)

	_, err := singleTopLevelDir(root)
	if err == nil {
		t.Fatal("expected an error when archive has more than one top-level directory")
	}
}

func TestSingleTopLevelDir_RejectsZeroDirs(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "just-a-file.txt"), []byte("x"), 0o644)

	_, err := singleTopLevelDir(root)
	if err == nil {
		t.Fatal("expected an error when archive has no top-level directory")
	}
}

// TestProgressReader_ThrottlesUpdates confirms onUpdate does NOT fire
// on every single Read call -- for a fast reader with many small
// reads, that would call it far more often than useful for any
// human-readable rendering (the whole point of throttling: no fancy
// animation, per the explicit ask).
func TestProgressReader_ThrottlesUpdates(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 1000)
	var calls int

	pr := &progressReader{
		r:     bytes.NewReader(data),
		total: int64(len(data)),
		onUpdate: func(read, total int64, final bool) {
			calls++
		},
	}

	buf := make([]byte, 10)
	for i := 0; i < 100; i++ {
		if _, err := pr.Read(buf); err != nil {
			break
		}
	}

	// 100 reads happen essentially instantly -- well under the
	// throttle interval -- so onUpdate should have fired far fewer
	// than 100 times (in practice: once, for the very first read,
	// since lastTick starts at zero value and the interval hasn't
	// elapsed since). The exact count isn't the point; confirming
	// it's NOT anywhere near 100 is.
	if calls >= 50 {
		t.Errorf("expected throttling to suppress most updates, but onUpdate fired %d times for 100 reads", calls)
	}
}

// TestProgressReader_AlwaysCallsFinalOnCompletion confirms the LAST
// call is always made with final=true when the read completes, even
// if it happens to fall within the throttle window -- callers need
// this unambiguous signal rather than guessing completion from
// read>=total, which breaks when total is unknown.
func TestProgressReader_AlwaysCallsFinalOnCompletion(t *testing.T) {
	data := []byte("hello world")
	var lastFinal bool
	var lastRead int64

	pr := &progressReader{
		r:     bytes.NewReader(data),
		total: int64(len(data)),
		onUpdate: func(read, total int64, final bool) {
			lastFinal = final
			lastRead = read
		},
	}

	buf := make([]byte, 4)
	for {
		_, err := pr.Read(buf)
		if err != nil {
			break
		}
	}

	if !lastFinal {
		t.Error("expected the final call to have final=true")
	}
	if lastRead != int64(len(data)) {
		t.Errorf("expected final read count to equal total bytes (%d), got %d", len(data), lastRead)
	}
}

// TestProgressReader_UnknownTotalPassedThrough confirms a -1 total
// (server didn't report Content-Length) is passed straight through to
// the callback, not silently converted to 0 or some other value that
// could be misread as a real total.
func TestProgressReader_UnknownTotalPassedThrough(t *testing.T) {
	data := []byte("some data")
	var sawTotal int64 = -999 // sentinel, should be overwritten

	pr := &progressReader{
		r:     bytes.NewReader(data),
		total: -1,
		onUpdate: func(read, total int64, final bool) {
			sawTotal = total
		},
	}

	buf := make([]byte, len(data))
	pr.Read(buf)
	pr.Read(buf) // second read to trigger EOF -> final call

	if sawTotal != -1 {
		t.Errorf("expected unknown total (-1) to be passed through unchanged, got %d", sawTotal)
	}
}

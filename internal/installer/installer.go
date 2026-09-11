// Package installer implements the atomic download -> verify ->
// extract -> place sequence for turning a resolved registry.Asset into
// a real, usable version folder. Deliberately decoupled from any
// specific vendor (registry.Provider) or tool (tooldef.Tool) -- this
// package only ever deals with "here's a URL, a checksum, and a
// destination," making it fully testable against a local fake HTTP
// server, without needing real network access or a real JDK archive.
//
// Design choices below are directly informed by researching real
// failure modes in Homebrew and SDKMAN (both long-established,
// widely-used tools):
//
//   - Never resume a partial download. SDKMAN has a real, still-open
//     bug (sdkman-cli#1288) where resuming via HTTP byte-range fails
//     permanently if the server doesn't support it, leaving an install
//     that can never complete without manual intervention. Every
//     attempt here starts a fresh download into a brand new temp file.
//   - Never use a persistent, reusable download cache. SDKMAN
//     (sdkman-cli#1005, "reinstallation of cached archives fail") and
//     Homebrew (multiple long-standing GitHub issues: "repeated
//     checksum mismatch... removing tarball does not resolve") have
//     both had real, recurring bugs from stale cached downloads
//     confusing later attempts. Every attempt's temp files are
//     uniquely named and fully discarded on any failure -- there is
//     nothing left behind to go stale.
//   - Always verify the checksum, and retry (fresh download) on either
//     a network failure OR a checksum mismatch -- matching Homebrew's
//     own documented behavior ("--retry: Retry if downloading fails or
//     re-download if the checksum... no longer matches").
//   - Never write anything to the final destination until everything
//     has been fully verified and extracted -- the ONLY operation that
//     touches TargetDir is a single atomic os.Rename at the very end.
package installer

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// ErrAlreadyExists is returned when TargetDir already exists -- Install
// never overwrites, matching the same non-destructive rule already
// established for `add` (design doc §10.2): a version already present
// is never silently replaced.
var ErrAlreadyExists = errors.New("installer: target already exists")

// Options configures a single Install call.
type Options struct {
	// URL is the archive's direct download link.
	URL string

	// Checksum is the expected checksum, lowercase hex, no prefix.
	Checksum string

	// Filename is the archive's own vendor-published filename (e.g.
	// "OpenJDK21U-jdk_x64_windows_hotspot_21.0.2_13.zip") -- used
	// SOLELY to pick the right extractor (see extractorFor), by the
	// archive's REAL file extension. Deliberately NOT derived from
	// the downloaded archive's own on-disk temp path -- confirmed
	// directly: downloadWithRetry saves to a randomly-named
	// os.CreateTemp("download-*") file, which never carries the
	// original extension at all, so there is no other reliable
	// source for this information. Required -- Install fails fast,
	// before any network activity, if this doesn't match a
	// recognized extension, rather than a confusing failure deep
	// inside extraction.
	Filename string

	// ChecksumAlgorithm names which hash Checksum is -- registry.SHA256
	// or registry.SHA1. Not every vendor publishes the same algorithm
	// (Temurin: SHA-256; Liberica: SHA-1 only, confirmed directly from
	// their own API's documented responses) -- this makes verification
	// correct regardless of which one resolved the asset, rather than
	// assuming SHA-256 universally. Defaults to registry.SHA256 if
	// left empty, preserving existing callers that predate this field.
	ChecksumAlgorithm string

	// TargetDir is the final, real directory this version should end
	// up at, e.g. ~/.sdkkeeper/candidates/java/JDK-21.0.2-temurin.
	// Must not already exist.
	TargetDir string

	// TempRoot is where scratch work happens, e.g.
	// ~/.sdkkeeper/tmp. MUST be on the same filesystem as TargetDir's
	// parent, or the final placement can't be a true atomic rename
	// (os.Rename silently degrades to non-atomic copy+delete across
	// filesystems on some platforms).
	TempRoot string

	// MaxAttempts is how many times to retry the download+verify cycle
	// on failure (network error or checksum mismatch) before giving
	// up. Each attempt is a completely fresh download -- see the
	// package doc for why partial-download resume is deliberately not
	// implemented.
	MaxAttempts int

	// RetryDelay is how long to wait between attempts. A simple fixed
	// delay, not exponential backoff -- with MaxAttempts kept small
	// (2-3), the added complexity of backoff isn't worth it.
	RetryDelay time.Duration

	// ExtractionProgressThreshold is how long extraction must run
	// before "Extracting..." actually prints, rather than staying
	// silent for a fast, sub-second extraction that would otherwise
	// flash the message by too quickly to read. Defaults to 400ms
	// when zero. Configurable (matching MaxAttempts/RetryDelay's own
	// pattern) specifically so tests can force either behavior
	// deterministically, without needing a genuinely slow disk or a
	// real wall-clock wait.
	ExtractionProgressThreshold time.Duration

	// HTTPClient defaults to http.DefaultClient when nil.
	HTTPClient *http.Client

	// Progress, if non-nil, is called with short human-readable status
	// updates ("Downloading...", "Verifying checksum...", etc.) --
	// entirely optional, safe to leave nil for tests or silent use.
	Progress func(string)

	// DownloadProgress, if non-nil, is called periodically DURING the
	// download with bytes read so far and the total (total is -1 if
	// the server didn't report Content-Length, e.g. chunked transfer
	// encoding -- callers should handle that by omitting a percentage
	// rather than showing something nonsensical like "58% of unknown").
	// final is true on the LAST call (the read completed, successfully
	// or not) -- callers use this to know unambiguously when to stop
	// updating in place, rather than guessing from read>=total, which
	// breaks when total is unknown. Throttled internally (see
	// progressReader) -- callers don't need their own rate-limiting.
	// Entirely optional, safe to leave nil.
	DownloadProgress func(readBytes, totalBytes int64, final bool)

	// ExtractionProgress, if non-nil, is called periodically DURING
	// extraction with the number of archive entries processed so far.
	// Unlike DownloadProgress, there's no known "total" here -- tar
	// archives don't declare an entry count upfront the way HTTP
	// responses declare Content-Length, and counting them would mean
	// reading the whole archive twice just to find out. final is true
	// on the last entry, matching DownloadProgress's own guarantee.
	// Whether this actually reaches a human depends on
	// ExtractionProgressThreshold -- see Install's own comment for why
	// that gating exists.
	ExtractionProgress func(count int64, final bool)
}

func (o Options) client() *http.Client {
	if o.HTTPClient != nil {
		return o.HTTPClient
	}
	return http.DefaultClient
}

func (o Options) progress(msg string) {
	if o.Progress != nil {
		o.Progress(msg)
	}
}

// Checksum algorithm names -- matches registry.SHA256/registry.SHA1
// exactly (same string values), kept as independent constants here
// rather than importing the registry package, preserving this
// package's deliberate decoupling from any vendor/asset concept (see
// the package doc).
const (
	sha256Algorithm = "sha256"
	sha1Algorithm   = "sha1"
	sha512Algorithm = "sha512"
)

func (o Options) checksumAlgorithm() string {
	if o.ChecksumAlgorithm != "" {
		return o.ChecksumAlgorithm
	}
	return sha256Algorithm
}

// newHasher returns the correct hash.Hash for algorithm, defaulting to
// SHA-256 for anything unrecognized (matches checksumAlgorithm's own
// default, so an empty/unset algorithm and an explicitly-empty one
// behave identically).
func newHasher(algorithm string) hash.Hash {
	switch algorithm {
	case sha1Algorithm:
		return sha1.New()
	case sha512Algorithm:
		return sha512.New()
	default:
		return sha256.New()
	}
}

func (o Options) extractionProgressThreshold() time.Duration {
	if o.ExtractionProgressThreshold > 0 {
		return o.ExtractionProgressThreshold
	}
	return 400 * time.Millisecond
}

// Install downloads, verifies, extracts, and atomically places the
// archive at opts.URL into opts.TargetDir. Returns ErrAlreadyExists
// immediately, before any network activity, if TargetDir already
// exists (checked via Lstat, so an existing symlink -- e.g. one
// created by `add` -- correctly counts as "already exists" too, even
// a dangling one).
func Install(ctx context.Context, opts Options) error {
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 3
	}
	if opts.RetryDelay <= 0 {
		opts.RetryDelay = 2 * time.Second
	}

	// Validated FIRST, before any network activity at all -- an
	// unrecognized archive format should fail fast and clearly, not
	// after a real download has already completed, deep inside
	// extraction with a confusing, indirect error.
	extract, err := extractorFor(opts.Filename)
	if err != nil {
		return err
	}

	if _, err := os.Lstat(opts.TargetDir); err == nil {
		return ErrAlreadyExists
	}

	if err := os.MkdirAll(opts.TempRoot, 0o755); err != nil {
		return fmt.Errorf("installer: could not create %s: %w", opts.TempRoot, err)
	}
	// Registered BEFORE the archivePath/extractDir defers below, so
	// (deferred functions run LIFO) this one runs LAST -- after
	// everything else that was inside TempRoot has already been
	// cleaned up. Best-effort: if TempRoot is now empty, remove the
	// directory itself too, rather than leaving an empty scratch
	// folder sitting at ~/.sdkkeeper/tmp indefinitely after every
	// completed install (a real question raised after actual use: an
	// empty leftover directory, while harmless, looks like unexplained
	// cruft to anyone inspecting ~/.sdkkeeper directly). If anything
	// is still inside it for any reason, this is a no-op -- never
	// removes a non-empty directory.
	defer func() {
		entries, err := os.ReadDir(opts.TempRoot)
		if err == nil && len(entries) == 0 {
			os.Remove(opts.TempRoot)
		}
	}()

	archivePath, err := downloadWithRetry(ctx, opts)
	if err != nil {
		return err
	}
	defer os.Remove(archivePath)

	extractDir, err := os.MkdirTemp(opts.TempRoot, "extract-*")
	if err != nil {
		return fmt.Errorf("installer: could not create extraction dir: %w", err)
	}
	defer os.RemoveAll(extractDir)

	// "Extracting..." only prints if extraction is STILL running after
	// a short threshold, rather than unconditionally -- a real JDK
	// archive extracts entirely on local disk (no network involved)
	// and typically finishes in well under a second on any modern
	// disk, meaning the message would usually flash by too fast to
	// actually be read, adding noise between the download bar and the
	// final confirmation without providing anything genuinely
	// readable. But if extraction ever DOES take a noticeable pause
	// (a slower disk, a network-mounted home directory, a much larger
	// future archive), staying completely silent would leave a gap
	// that could make the tool look hung -- so the message still
	// appears, honestly, exactly when it's actually warranted. The
	// timer is stopped as soon as extraction finishes, whichever
	// happens first; no message, no goroutine leak, no printing race
	// with anything else (the main goroutine is synchronously blocked
	// inside extractTarGz for this entire window, so there's no
	// concurrent-print risk).
	//
	// crossedThreshold is shared between the timer (below) and
	// extractTarGz's own progress callback: extractTarGz always
	// reports its count, throttled, regardless of speed -- it's the
	// gating here, not extractTarGz itself, that decides whether any
	// of that actually reaches the caller. Once the threshold fires
	// OR extraction reports its final count, updates start flowing
	// through; before that, they're silently dropped, keeping a fast
	// extraction exactly as quiet as before.
	var crossedThreshold atomic.Bool
	extractErr := runWithThresholdedProgress(
		opts.extractionProgressThreshold(),
		func() { crossedThreshold.Store(true) },
		func() error {
			return extract(archivePath, extractDir, func(count int64, final bool) {
				// Deliberately NOT "final || crossedThreshold.Load()"
				// -- a genuinely fast extraction that never crosses
				// the threshold should stay COMPLETELY silent,
				// including its own final call. Forwarding the final
				// call unconditionally would mean a fast extraction
				// still shows one line at the very end, defeating the
				// whole point of staying silent for the common case.
				if opts.ExtractionProgress != nil && crossedThreshold.Load() {
					opts.ExtractionProgress(count, final)
				}
			})
		},
	)

	if extractErr != nil {
		return fmt.Errorf("installer: extraction failed: %w", extractErr)
	}

	innerDir, err := singleTopLevelDir(extractDir)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(opts.TargetDir), 0o755); err != nil {
		return fmt.Errorf("installer: could not create %s: %w", filepath.Dir(opts.TargetDir), err)
	}

	// The one and only operation that touches TargetDir. Same
	// filesystem (both under TempRoot's and TargetDir's shared
	// ancestor, per the Options.TempRoot doc comment) means this is a
	// true atomic rename, not copy+delete.
	if err := os.Rename(innerDir, opts.TargetDir); err != nil {
		return fmt.Errorf("installer: could not place %s: %w", opts.TargetDir, err)
	}

	return nil
}

// progressReader wraps an io.Reader, calling onUpdate periodically with
// bytes read so far and the total (as reported by Content-Length; -1
// if the server didn't send one, e.g. chunked transfer encoding).
// Throttled to a few times a second (not on every Read call, which
// could be many times per second for a fast connection with a small
// buffer size) -- deliberately NOT a fancy animated progress bar, per
// the explicit ask; this just rate-limits how often the callback
// fires so the caller's own rendering isn't overwhelmed. Always calls
// onUpdate one final time when the read completes (err != nil, most
// commonly io.EOF), with final=true, regardless of the time-based
// throttle -- so the caller always gets an unambiguous "this is truly
// done" signal, rather than having to guess from read>=total, which
// breaks when total is unknown (-1).
type progressReader struct {
	r        io.Reader
	total    int64
	read     int64
	lastTick time.Time
	onUpdate func(read, total int64, final bool)
}

const progressUpdateInterval = 150 * time.Millisecond

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.read += int64(n)
	if err != nil {
		p.onUpdate(p.read, p.total, true)
		p.lastTick = time.Now()
	} else if time.Since(p.lastTick) >= progressUpdateInterval {
		p.onUpdate(p.read, p.total, false)
		p.lastTick = time.Now()
	}
	return n, err
}

// runWithThresholdedProgress runs work, calling onSlow (if non-nil)
// only if work is STILL running after threshold -- so a fast operation
// stays silent (avoiding a message that would just flash by unread),
// while a genuinely slow one gets an honest, timely status message
// instead of leaving the user looking at a silent gap. onSlow's timer
// is stopped as soon as work finishes, whichever happens first -- no
// message, no goroutine leak. Extracted as its own function
// specifically so this timing logic can be tested directly, with a
// deterministic fake `work` (e.g. a controlled time.Sleep), rather
// than depending on real, inherently variable filesystem/extraction
// speed to land on either side of the threshold reliably.
func runWithThresholdedProgress(threshold time.Duration, onSlow func(), work func() error) error {
	done := make(chan struct{})
	go func() {
		select {
		case <-time.After(threshold):
			if onSlow != nil {
				onSlow()
			}
		case <-done:
		}
	}()

	err := work()
	close(done)
	return err
}

// downloadWithRetry performs the fetch+verify cycle, retrying on
// either a network failure or a checksum mismatch, up to
// opts.MaxAttempts times. Every attempt is a completely fresh
// download into a brand new temp file -- see the package doc.
func downloadWithRetry(ctx context.Context, opts Options) (string, error) {
	var lastErr error

	for attempt := 1; attempt <= opts.MaxAttempts; attempt++ {
		if attempt > 1 {
			opts.progress(fmt.Sprintf("Retrying download (attempt %d/%d)...", attempt, opts.MaxAttempts))
			select {
			case <-time.After(opts.RetryDelay):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		// No separate "Downloading..." announcement on the first
		// attempt -- the DownloadProgress bar itself (see install.go's
		// renderDownloadProgress) now carries a "Downloading" prefix
		// as part of its own first render, so the announcement and the
		// bar are one line, not two.

		path, err := downloadOnce(ctx, opts)
		if err == nil {
			return path, nil
		}
		lastErr = err
	}

	return "", fmt.Errorf("installer: download failed after %d attempts: %w", opts.MaxAttempts, lastErr)
}

// downloadOnce performs exactly one fresh download attempt into a new
// temp file, verifying its checksum before returning. On ANY failure
// (network error or checksum mismatch), the partial/incorrect temp
// file is removed before returning -- nothing is ever left behind for
// a later attempt to accidentally reuse.
func downloadOnce(ctx context.Context, opts Options) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.URL, nil)
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}

	resp, err := opts.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	f, err := os.CreateTemp(opts.TempRoot, "download-*")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	path := f.Name()

	// resp.ContentLength is -1 when the server didn't report a size
	// (e.g. chunked transfer encoding) -- progressReader passes that
	// straight through so the caller can detect it and omit a
	// percentage rather than showing something nonsensical.
	var body io.Reader = resp.Body
	if opts.DownloadProgress != nil {
		body = &progressReader{
			r:        resp.Body,
			total:    resp.ContentLength,
			onUpdate: opts.DownloadProgress,
		}
	}

	hasher := newHasher(opts.checksumAlgorithm())
	_, copyErr := io.Copy(io.MultiWriter(f, hasher), body)
	closeErr := f.Close()

	if copyErr != nil {
		os.Remove(path)
		return "", fmt.Errorf("downloading body: %w", copyErr)
	}
	if closeErr != nil {
		os.Remove(path)
		return "", fmt.Errorf("closing temp file: %w", closeErr)
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	want := strings.ToLower(opts.Checksum)
	if got != want {
		os.Remove(path)
		return "", fmt.Errorf("checksum mismatch (%s): expected %s, got %s", opts.checksumAlgorithm(), want, got)
	}

	return path, nil
}

// extractor is the shape shared by extractTarGz and extractZip --
// letting Install pick the right one for a given archive, by the
// archive's REAL file extension (Options.Filename, the vendor's own
// published name), not by host OS. Deliberately NOT a switch on
// runtime.GOOS: a future vendor could plausibly package a given OS
// differently than expected (or a new OS/format combination could be
// added later), so this dispatches on the actual file being
// extracted, never an assumption about what SHOULD have been
// downloaded for the current platform.
type extractor func(archivePath, destDir string, onProgress func(count int64, final bool)) error

// extractorFor returns the right extractor for filename's extension,
// or a clear, specific error naming the unrecognized extension --
// checked via Install before any download happens (see its own
// comment), so a genuinely new/unexpected archive format fails fast
// and legibly, rather than surfacing as a confusing "not a valid
// gzip archive" (or similar) deep inside extraction, after real
// network work has already completed.
func extractorFor(filename string) (extractor, error) {
	switch {
	case strings.HasSuffix(filename, ".tar.gz"):
		return extractTarGz, nil
	case strings.HasSuffix(filename, ".zip"):
		return extractZip, nil
	default:
		return nil, fmt.Errorf("installer: unrecognized archive format for %q (expected .tar.gz or .zip)", filename)
	}
}

// extractTarGz extracts a .tar.gz archive into destDir. Rejects any
// entry whose resolved path would escape destDir (a "Zip Slip"-style
// path-traversal guard) -- defense in depth even though the archive's
// checksum has already been verified against the vendor's published
// value, since checksum verification confirms integrity, not that the
// archive's internal structure is well-formed.
//
// onProgress, if non-nil, is called with the number of entries
// processed so far -- throttled to the same cadence as download
// progress (progressUpdateInterval), plus always once more on the
// final entry (final=true), regardless of the throttle, so the
// caller's last update always reflects the true final count. Safe to
// pass nil (e.g. from tests that don't care about progress).
func extractTarGz(archivePath, destDir string, onProgress func(count int64, final bool)) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("not a valid gzip archive: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var count int64
	var lastTick time.Time
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			if onProgress != nil {
				onProgress(count, true)
			}
			return nil
		}
		if err != nil {
			return err
		}

		target := filepath.Join(destDir, hdr.Name)
		if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry %q escapes destination directory", hdr.Name)
		}

		// The POSIX permission bits used throughout this function
		// (os.FileMode(hdr.Mode), the literal 0o755/0o644 values
		// below) are a genuine no-op on Windows -- NTFS has no
		// concept of Unix-style rwx bits at all, using ACLs instead.
		// This is NOT a bug needing a Windows-specific code path:
		// Go's own os package already handles this gracefully and
		// silently cross-platform (confirmed directly from Go's own
		// os package documentation for Chmod/OpenFile/MkdirAll on
		// Windows) -- the calls below simply have no observable
		// effect there, rather than erroring or corrupting anything.
		// Noted here explicitly so a future reader investigating a
		// Windows-specific report isn't left wondering why permission
		// bits appear to do nothing.
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			// Best-effort: a pre-existing symlink at this exact path
			// from an earlier interrupted attempt would make Symlink
			// fail -- but each attempt uses a brand new extractDir, so
			// this genuinely can't happen in practice.
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		}

		count++
		if onProgress != nil && time.Since(lastTick) >= progressUpdateInterval {
			onProgress(count, false)
			lastTick = time.Now()
		}
	}
}

// singleTopLevelDir returns the one directory entry directly inside
// root, erroring if there isn't exactly one. Real JDK archives always
// extract to a single top-level directory (e.g. "jdk-21.0.2+13/...")
// -- that inner directory, not the temp wrapper around it, is what
// gets placed at TargetDir.
func singleTopLevelDir(root string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", fmt.Errorf("installer: could not read extracted contents: %w", err)
	}

	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}

	if len(dirs) != 1 {
		return "", fmt.Errorf("installer: expected exactly one top-level directory in archive, found %d", len(dirs))
	}

	return filepath.Join(root, dirs[0]), nil
}

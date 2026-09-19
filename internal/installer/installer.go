// Package installer implements the atomic download -> verify ->
// extract -> place sequence for turning a resolved registry.Asset
// into a real, usable version folder. Decoupled from any specific
// vendor or tool -- it only deals with "here's a URL, a checksum, and
// a destination," so it's fully testable against a local fake HTTP
// server.
//
// Design choices, informed by real failure modes in Homebrew and
// SDKMAN:
//
//   - Resume support: falls back to a clean, full restart the moment
//     a server doesn't honor a Range request, avoiding SDKMAN's own
//     cited bug (sdkman-cli#1288) where resume fails permanently and
//     the install can never complete.
//   - No persistent download cache: removed after real, unbounded
//     disk usage (every install left a growing copy behind with no
//     way to reclaim it) outweighed the benefit, given how uncommon
//     reinstalling the exact same version is for a tool that
//     encourages keeping multiple versions side by side. Resume
//     support is unaffected -- a different feature with no
//     persistent state of its own.
//   - Always verify the checksum, and retry on either a network
//     failure or a checksum mismatch, matching Homebrew's own
//     documented --retry behavior.
//   - Nothing touches the final destination until everything is
//     verified and extracted -- the only operation on TargetDir is a
//     single atomic os.Rename at the end.
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

// ErrAlreadyExists is returned when TargetDir already exists --
// Install never overwrites an existing version.
var ErrAlreadyExists = errors.New("installer: target already exists")

// Options configures a single Install call.
type Options struct {
	// URL is the archive's direct download link.
	URL string

	// Checksum is the expected checksum, lowercase hex, no prefix.
	Checksum string

	// Filename is the archive's vendor-published filename, used solely
	// to pick the right extractor by its real file extension --
	// downloadWithRetry saves to a randomly-named temp file with no
	// extension of its own, so there's no other source for this.
	// Required; Install fails fast if it's unrecognized.
	Filename string

	// ChecksumAlgorithm names which hash Checksum is -- registry.SHA256
	// or registry.SHA1. Not every vendor publishes the same one
	// (Temurin: SHA-256; Liberica: SHA-1 only). Defaults to SHA256 if
	// empty.
	ChecksumAlgorithm string

	// TargetDir is the final directory this version ends up at, e.g.
	// ~/.sdkkeeper/candidates/java/JDK-21.0.2-temurin. Must not
	// already exist.
	TargetDir string

	// TempRoot is where scratch work happens, e.g. ~/.sdkkeeper/tmp.
	// Must be on the same filesystem as TargetDir's parent, or the
	// final placement can't be a true atomic rename.
	TempRoot string

	// MaxAttempts is how many times to retry the download+verify cycle
	// before giving up. Each retry resumes where the previous attempt
	// left off when the server supports it, falling back to a fresh
	// restart otherwise -- see downloadOnce.
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

// progressReader wraps an io.Reader, calling onUpdate periodically
// with bytes read so far and the total (-1 if the server didn't send
// Content-Length). Throttled to a few times a second so the caller's
// rendering isn't overwhelmed, but always calls onUpdate one final
// time with final=true when the read completes, regardless of the
// throttle -- an unambiguous "done" signal that doesn't rely on
// read>=total, which breaks when total is unknown.
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

// runWithThresholdedProgress runs work, calling onSlow only if work
// is still running after threshold -- a fast operation stays silent,
// a slow one gets a timely status message instead of a silent gap.
// The timer stops as soon as work finishes, whichever comes first.
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
	// Path to a partial download worth resuming from, "" if none --
	// carried across retry attempts within this same call. Cleaned up
	// on any path that returns without a successful final download,
	// so a genuinely unresumable failure never leaves a stray temp
	// file behind, matching the ORIGINAL behavior's own guarantee.
	var resumePath string

	for attempt := 1; attempt <= opts.MaxAttempts; attempt++ {
		if attempt > 1 {
			opts.progress(fmt.Sprintf("Retrying download (attempt %d/%d)...", attempt, opts.MaxAttempts))
			select {
			case <-time.After(opts.RetryDelay):
			case <-ctx.Done():
				if resumePath != "" {
					os.Remove(resumePath)
				}
				return "", ctx.Err()
			}
		}
		// No separate "Downloading..." announcement on the first
		// attempt -- the DownloadProgress bar itself (see install.go's
		// renderDownloadProgress) now carries a "Downloading" prefix
		// as part of its own first render, so the announcement and the
		// bar are one line, not two.

		path, nextResumePath, err := downloadOnce(ctx, opts, resumePath)
		if err == nil {
			return path, nil
		}
		resumePath = nextResumePath
		lastErr = err
	}

	if resumePath != "" {
		os.Remove(resumePath)
	}
	return "", fmt.Errorf("installer: download failed after %d attempts: %w", opts.MaxAttempts, lastErr)
}

// downloadOnce performs one download attempt, verifying the result's
// checksum before returning. resumePath, if non-empty, names a
// partial file from a prior failed attempt worth resuming via an HTTP
// Range request; pass "" for a fresh attempt.
//
// Returns (path, "", nil) on success. On failure, returns ("",
// resumableAt, err) -- resumableAt names a partial file worth passing
// to the next attempt (empty if the server doesn't support ranges, or
// the completed file was simply wrong, e.g. a checksum mismatch).
//
// hash.Hash can't save/restore its state across a new HTTP
// request/response cycle, so a resumed attempt re-reads the bytes
// already on disk through a fresh hasher once before appending new
// data -- simple, correct, and negligible next to the network time
// already saved.
func downloadOnce(ctx context.Context, opts Options, resumePath string) (path string, resumableAt string, err error) {
	var startOffset int64
	if resumePath != "" {
		if info, statErr := os.Stat(resumePath); statErr == nil {
			startOffset = info.Size()
		} else {
			// The partial file is gone (deleted out from under us,
			// or never existed) -- fall back to starting fresh,
			// rather than failing outright over something recoverable.
			resumePath = ""
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.URL, nil)
	if err != nil {
		return "", "", fmt.Errorf("building request: %w", err)
	}
	if startOffset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", startOffset))
	}

	resp, err := opts.client().Do(req)
	if err != nil {
		// A genuine network-level failure before any response at all
		// -- the partial file, if any, is untouched and still good to
		// resume from on the next attempt.
		return "", resumePath, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	var f *os.File
	var hasher hash.Hash
	var finalPath string

	if startOffset > 0 && resp.StatusCode == http.StatusPartialContent {
		// The server genuinely honored the Range request -- resume
		// for real: append to the existing partial file, having first
		// rebuilt the hasher's state from what's already on disk (see
		// this function's own doc comment for why that's done this
		// way rather than trying to persist hash state directly).
		f, err = os.OpenFile(resumePath, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return "", "", fmt.Errorf("reopening partial download: %w", err)
		}
		finalPath = resumePath
		hasher = newHasher(opts.checksumAlgorithm())
		existing, openErr := os.Open(resumePath)
		if openErr != nil {
			f.Close()
			return "", "", fmt.Errorf("reopening partial download for hashing: %w", openErr)
		}
		_, hashErr := io.Copy(hasher, existing)
		existing.Close()
		if hashErr != nil {
			f.Close()
			return "", "", fmt.Errorf("rehashing partial download: %w", hashErr)
		}
	} else {
		// Either a genuinely fresh attempt (resumePath == ""), or the
		// server didn't honor the Range request (some servers/CDNs
		// don't support it at all, and correctly respond 200 with the
		// full body instead of 206) -- either way, start completely
		// from scratch. Any stale partial file is discarded outright,
		// never silently mixed with a full-content response starting
		// over from byte zero.
		if resumePath != "" {
			os.Remove(resumePath)
			startOffset = 0
		}
		if resp.StatusCode != http.StatusOK {
			return "", "", fmt.Errorf("unexpected status %d", resp.StatusCode)
		}
		f, err = os.CreateTemp(opts.TempRoot, "download-*")
		if err != nil {
			return "", "", fmt.Errorf("creating temp file: %w", err)
		}
		finalPath = f.Name()
		hasher = newHasher(opts.checksumAlgorithm())
	}

	// resp.ContentLength is -1 when the server didn't report a size
	// (e.g. chunked transfer encoding) -- progressReader passes that
	// straight through so the caller can detect it and omit a
	// percentage rather than showing something nonsensical.
	//
	// total/read are adjusted for a genuinely resumed request so
	// progress reflects the FULL file (bytes already on disk plus
	// what's left to fetch), not just this one response's own
	// remaining byte count starting back over from zero.
	total := resp.ContentLength
	if startOffset > 0 && resp.StatusCode == http.StatusPartialContent && total >= 0 {
		total += startOffset
	}
	var body io.Reader = resp.Body
	if opts.DownloadProgress != nil {
		body = &progressReader{
			r:        resp.Body,
			total:    total,
			read:     startOffset,
			onUpdate: opts.DownloadProgress,
		}
	}

	_, copyErr := io.Copy(io.MultiWriter(f, hasher), body)
	closeErr := f.Close()

	if copyErr != nil {
		// A genuine network-level failure mid-stream -- the
		// (now-longer) partial file is still good, worth resuming
		// from on the next attempt.
		return "", finalPath, fmt.Errorf("downloading body: %w", copyErr)
	}
	if closeErr != nil {
		return "", finalPath, fmt.Errorf("closing temp file: %w", closeErr)
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	want := strings.ToLower(opts.Checksum)
	if got != want {
		// The file IS complete, but simply wrong -- nothing "partial"
		// about it to resume, so it's discarded outright, matching
		// the exact original behavior for this specific failure case.
		os.Remove(finalPath)
		return "", "", fmt.Errorf("checksum mismatch (%s): expected %s, got %s", opts.checksumAlgorithm(), want, got)
	}

	return finalPath, "", nil
}

// extractor is the shape shared by extractTarGz and extractZip,
// letting Install pick the right one by the archive's real file
// extension (Options.Filename), not by host OS -- a future vendor
// could package a given OS differently than expected.
type extractor func(archivePath, destDir string, onProgress func(count int64, final bool)) error

// extractorFor returns the right extractor for filename's extension,
// or a clear error naming it -- checked before any download happens,
// so an unexpected format fails fast rather than deep in extraction.
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
// entry whose resolved path would escape destDir (a Zip Slip guard)
// -- defense in depth, since checksum verification confirms integrity
// but not internal structure.
//
// onProgress, if non-nil, is called with entries processed so far,
// throttled like download progress, plus always once more on the
// final entry. Safe to pass nil.
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

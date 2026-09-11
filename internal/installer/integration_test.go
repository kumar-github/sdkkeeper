package installer_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sdkkeeper/internal/installer"
	"sdkkeeper/internal/registry/temurin"
)

// TestFullPipeline_TemurinToInstaller proves the actual composition
// this project's real `sk install java <version>` command performs:
// registry/temurin resolves an Asset from a (fake, local) vendor API,
// and that Asset's fields -- specifically asset.URL, used AS-IS below,
// not reconstructed -- feed directly into installer.Install. This is
// the one thing neither package's own unit tests individually verify:
// that the data handed from one to the other is exactly what's
// expected on the receiving end.
//
// This sandbox's network allowlist doesn't reach api.adoptium.net, so
// this cannot include the REAL live API -- but the fake server below
// serves a byte-for-byte realistic response shape (confirmed against
// Adoptium's own published documentation), and the actual
// temurin.Provider + installer.Install code runs completely for real
// against it, with nothing else faked or mocked.
func TestFullPipeline_TemurinToInstaller(t *testing.T) {
	archive, checksum := buildRealTarGz(t, "jdk-21.0.2+13", map[string]string{
		"bin/java": "fake java binary content",
	})

	// httptest.NewUnstartedServer lets us learn the server's own URL
	// BEFORE starting it, so the JSON handler below can correctly
	// embed the real archive URL (not a placeholder) -- this is what
	// makes it possible to use asset.URL, unmodified, in the actual
	// Install call further down.
	mux := http.NewServeMux()
	server := httptest.NewUnstartedServer(mux)
	baseURL := "http://" + server.Listener.Addr().String()

	mux.HandleFunc("/v3/assets/feature_releases/21/ga", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{
			"version_data": {"openjdk_version": "21.0.2+13", "semver": "21.0.2+13"},
			"binaries": [{"package": {
				"name": "OpenJDK21U-jdk_x64_mac_hotspot_21.0.2_13.tar.gz",
				"link": "` + baseURL + `/archive.tar.gz",
				"checksum": "` + checksum + `"
			}}]
		}]`))
	})
	mux.HandleFunc("/archive.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	})

	server.Start()
	defer server.Close()

	provider := &temurin.Provider{BaseURL: server.URL}
	asset, err := provider.ResolveAsset(context.Background(), "21.0.2", "darwin", "amd64")
	if err != nil {
		t.Fatalf("ResolveAsset failed: %v", err)
	}
	if asset.URL != baseURL+"/archive.tar.gz" {
		t.Fatalf("sanity check failed: asset.URL = %q, expected %q", asset.URL, baseURL+"/archive.tar.gz")
	}

	tmpRoot := t.TempDir()
	targetDir := filepath.Join(tmpRoot, "candidates", "java", "JDK-21.0.2")

	// Deliberately using asset.URL and asset.Checksum AS RETURNED by
	// temurin, not reconstructed -- this is the actual point of the test.
	err = installer.Install(context.Background(), installer.Options{
		URL:       asset.URL,
		Filename:  asset.Filename,
		Checksum:  asset.Checksum,
		TargetDir: targetDir,
		TempRoot:  filepath.Join(tmpRoot, "tmp"),
	})
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(targetDir, "bin", "java"))
	if err != nil {
		t.Fatalf("expected installed file to exist: %v", err)
	}
	if string(content) != "fake java binary content" {
		t.Errorf("unexpected installed content: %s", content)
	}
}

func buildRealTarGz(t *testing.T, topDir string, files map[string]string) ([]byte, string) {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	tw.WriteHeader(&tar.Header{Name: topDir + "/", Typeflag: tar.TypeDir, Mode: 0o755})
	for name, content := range files {
		full := topDir + "/" + name
		tw.WriteHeader(&tar.Header{Name: full, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))})
		tw.Write([]byte(content))
	}
	tw.Close()
	gz.Close()

	data := buf.Bytes()
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:])
}

// TestDownloadProgress_RealisticChunkedTransfer proves the download
// progress callback genuinely fires multiple times, with real,
// changing byte counts, against an ACTUAL slow, chunked HTTP response
// -- not just a synthetic in-memory byte reader (see
// TestProgressReader_* in installer_test.go for that unit-level
// coverage). Writes a realistic-sized (~2MB) archive in small,
// deliberately-delayed chunks, matching what a real network download
// looks like, and confirms: more than one distinct update was
// observed, the byte counts genuinely increased across calls (not
// stuck or reported out of order), and the very last call is
// final=true with read==total.
func TestDownloadProgress_RealisticChunkedTransfer(t *testing.T) {
	// Random, non-compressible bytes -- a repeated character (e.g.
	// strings.Repeat("x", ...)) was tried first and gzip-compressed
	// down to almost nothing (2174 bytes for a "2MB" payload), since
	// gzip crushes highly repetitive data -- completely defeating the
	// point of testing a realistic transfer size. Real JDK archives
	// are mostly compiled binaries, which don't compress like that.
	payload := make([]byte, 2*1024*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("failed to generate random payload: %v", err)
	}
	archive, checksum := buildRealTarGz(t, "jdk-21.0.2", map[string]string{
		"bin/java": string(payload),
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(archive)))
		chunkSize := 32 * 1024
		for i := 0; i < len(archive); i += chunkSize {
			end := i + chunkSize
			if end > len(archive) {
				end = len(archive)
			}
			w.Write(archive[i:end])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			// 8ms per 32KB chunk -> ~64 chunks means a total transfer
			// time comfortably over progressUpdateInterval (150ms),
			// guaranteeing at least one THROTTLED intermediate update
			// beyond just the first and final calls. An earlier
			// version of this test used 2ms/chunk (~128ms total,
			// under the throttle window) and incorrectly expected
			// multiple updates anyway -- that was a flawed test
			// assumption, not a bug: a transfer that fast genuinely
			// should only produce first+final, which is correct,
			// intended throttling behavior, not a failure.
			time.Sleep(8 * time.Millisecond)
		}
	}))
	defer server.Close()

	tmpRoot := t.TempDir()

	var reads []int64
	var lastFinal bool
	var lastTotal int64
	start := time.Now()

	err := installer.Install(context.Background(), installer.Options{
		URL:       server.URL,
		Filename:  "archive.tar.gz",
		Checksum:  checksum,
		TargetDir: filepath.Join(tmpRoot, "candidates", "java", "JDK-21.0.2"),
		TempRoot:  filepath.Join(tmpRoot, "tmp"),
		DownloadProgress: func(read, total int64, final bool) {
			reads = append(reads, read)
			lastFinal = final
			lastTotal = total
			t.Logf("progress at t=%v: read=%d total=%d final=%v", time.Since(start), read, total, final)
		},
	})
	t.Logf("Install returned at t=%v, err=%v", time.Since(start), err)
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	if len(reads) < 2 {
		t.Fatalf("expected multiple distinct progress updates for a throttled, chunked 2MB download, got %d", len(reads))
	}
	for i := 1; i < len(reads); i++ {
		if reads[i] < reads[i-1] {
			t.Errorf("expected byte counts to be non-decreasing across calls, got %v", reads)
			break
		}
	}
	if !lastFinal {
		t.Error("expected the last recorded call to have final=true")
	}
	if reads[len(reads)-1] != lastTotal {
		t.Errorf("expected the final call's read count (%d) to equal total (%d)", reads[len(reads)-1], lastTotal)
	}
}

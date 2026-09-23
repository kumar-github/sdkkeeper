package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"sdkkeeper/internal/registry"
	"sdkkeeper/internal/tooldef"
)

// TestClassifyInstallArg covers the version_required vs
// vendor_required split: no version at all vs. a version given with
// the vendor still ambiguous.
func TestClassifyInstallArg(t *testing.T) {
	java := mustTool(t, "java")
	maven := mustTool(t, "maven")

	cases := []struct {
		name string
		tool tooldef.Tool
		arg  string
		want ErrorCode // "" means success expected
	}{
		{"full identifier resolves", java, "21.0.2-temurin", ""},
		{"bare version resolves for single-vendor tool", maven, "3.9.9", ""},
		{"empty arg", java, "", ErrCodeVersionRequired},
		{"bare vendor name, no version", java, "temurin", ErrCodeVersionRequired},
		{"version given, no matching vendor", java, "21.0.2", ErrCodeVendorRequired},
		{"incomplete single-vendor version", maven, "3", ErrCodeVersionRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, jerr := classifyInstallArg(tc.tool, tc.arg)
			if tc.want == "" {
				if jerr != nil {
					t.Fatalf("expected success, got: %+v", jerr)
				}
				return
			}
			if jerr == nil || jerr.Code != tc.want {
				t.Fatalf("expected %s, got: %+v", tc.want, jerr)
			}
		})
	}
}

func TestResolveInstallJSON_Errors(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")

	cases := []struct {
		name    string
		tool    string
		version string
		want    ErrorCode
	}{
		{"unknown tool", "not-a-real-tool", "1.0", ErrCodeAmbiguousTool},
		{"tool with no install providers", "node", "1.0.0", ErrCodeNotFound},
		{"no version given", "java", "", ErrCodeVersionRequired},
		{"version given, vendor ambiguous", "java", "21.0.2", ErrCodeVendorRequired},
		{"already installed", "java", "21.0.2-temurin", ErrCodeAlreadyInstalled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, jerr := resolveInstallJSON(context.Background(), tc.tool, tc.version)
			if jerr == nil || jerr.Code != tc.want {
				t.Fatalf("expected %s, got: %+v", tc.want, jerr)
			}
		})
	}
}

// buildFakeTarGz creates a minimal, valid .tar.gz and returns its
// bytes and SHA256 checksum, so installer.Install's real extractor
// and checksum verification have something genuine to work against.
func buildFakeTarGz(t *testing.T) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	if err := tw.WriteHeader(&tar.Header{Name: "fake-1.0.0/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatalf("writing dir header: %v", err)
	}
	content := "#!/bin/sh\necho fake\n"
	if err := tw.WriteHeader(&tar.Header{Name: "fake-1.0.0/bin/fake", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatalf("writing file header: %v", err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatalf("writing file content: %v", err)
	}
	tw.Close()
	gz.Close()

	data := buf.Bytes()
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:])
}

// fakeInstallProvider is a registry.Provider stub whose ResolveAsset
// points at a real local httptest server, so the happy path exercises
// a genuine download/verify/extract/place cycle.
type fakeInstallProvider struct {
	name  string
	asset registry.Asset
}

func (f *fakeInstallProvider) Name() string { return f.name }
func (f *fakeInstallProvider) ResolveAsset(ctx context.Context, version, osName, arch string) (registry.Asset, error) {
	return f.asset, nil
}
func (f *fakeInstallProvider) ListPatchVersions(ctx context.Context, major, osName, arch string) ([]string, error) {
	return nil, nil
}
func (f *fakeInstallProvider) ListMajorVersions(ctx context.Context) ([]string, error) {
	return nil, nil
}
func (f *fakeInstallProvider) ListMajorVersionsWithLTS(ctx context.Context) ([]registry.MajorVersionInfo, error) {
	return nil, nil
}

// withTempProvider registers provider under toolName in
// providersByTool for the test's duration, restoring whatever was
// there before.
func withTempProvider(t *testing.T, toolName string, provider registry.Provider) {
	t.Helper()
	original, hadOriginal := providersByTool[toolName]
	providersByTool[toolName] = []namedProvider{{provider.Name(), provider}}
	t.Cleanup(func() {
		if hadOriginal {
			providersByTool[toolName] = original
		} else {
			delete(providersByTool, toolName)
		}
	})
}

// withTempProviders is withTempProvider for a genuinely multi-vendor
// tool: registers ALL of providers, in order (the same order
// vendorNamesFor and the vendor picker's own display both follow).
// withTempProvider (singular) always leaves exactly one provider
// registered, which silently makes hasSingleVendor true and skips the
// vendor picker entirely -- the wrong fixture for any test that needs
// the vendor-picker branch to actually run.
func withTempProviders(t *testing.T, toolName string, providers ...registry.Provider) {
	t.Helper()
	original, hadOriginal := providersByTool[toolName]
	nps := make([]namedProvider, len(providers))
	for i, p := range providers {
		nps[i] = namedProvider{p.Name(), p}
	}
	providersByTool[toolName] = nps
	t.Cleanup(func() {
		if hadOriginal {
			providersByTool[toolName] = original
		} else {
			delete(providersByTool, toolName)
		}
	})
}

func newFakeInstallServer(t *testing.T, archive []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	t.Cleanup(server.Close)
	return server
}

// TestResolveInstallJSON_RealDownloadInstallsAndReportsInstalled is
// the happy-path proof: a real HTTP download, checksum verification,
// and extraction, confirming the payload and a repeat install both
// behave correctly.
func TestResolveInstallJSON_RealDownloadInstallsAndReportsInstalled(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	archive, checksum := buildFakeTarGz(t)
	server := newFakeInstallServer(t, archive)
	withTempProvider(t, "kafka", &fakeInstallProvider{
		name: "apache",
		asset: registry.Asset{
			URL:               server.URL,
			Filename:          "fake-1.0.0.tar.gz",
			Checksum:          checksum,
			ChecksumAlgorithm: registry.SHA256,
		},
	})

	data, jerr := resolveInstallJSON(context.Background(), "kafka", "1.0.0")
	if jerr != nil {
		t.Fatalf("expected a successful install, got error: %+v", jerr)
	}
	if data.Action != string(actionInstalled) || data.Version != "1.0.0" {
		t.Errorf("unexpected payload: %+v", data)
	}
	if data.Vendor != nil {
		t.Errorf("expected a nil vendor for a single-provider tool, got %q", *data.Vendor)
	}
	if data.Bytes != int64(len(archive)) {
		t.Errorf("expected bytes=%d, got %d", len(archive), data.Bytes)
	}
	if data.Checksum != checksum {
		t.Errorf("expected checksum=%q, got %q", checksum, data.Checksum)
	}
	if data.ChecksumAlgorithm != "SHA-256" {
		t.Errorf("expected checksumAlgorithm=SHA-256 (display form, not the raw registry.SHA256 constant), got %q", data.ChecksumAlgorithm)
	}

	kafka := mustTool(t, "kafka")
	wantPath := filepath.Join(kafka.CandidateRoot(), kafka.FolderPrefix+"1.0.0")
	if data.Path != wantPath {
		t.Errorf("expected path=%q, got %q", wantPath, data.Path)
	}

	// Confirms the result is real, on disk -- not just the payload.
	_, jerr = resolveInstallJSON(context.Background(), "kafka", "1.0.0")
	if jerr == nil || jerr.Code != ErrCodeAlreadyInstalled {
		t.Fatalf("expected a repeat install to be already_installed, got: %+v", jerr)
	}
}

func TestResolveInstallJSON_ChecksumMismatchIsChecksumMismatch(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	archive, _ := buildFakeTarGz(t)
	server := newFakeInstallServer(t, archive)
	withTempProvider(t, "kafka", &fakeInstallProvider{
		name: "apache",
		asset: registry.Asset{
			URL:               server.URL,
			Filename:          "fake-1.0.0.tar.gz",
			Checksum:          "0000000000000000000000000000000000000000000000000000000000000000",
			ChecksumAlgorithm: registry.SHA256,
		},
	})

	_, jerr := resolveInstallJSON(context.Background(), "kafka", "2.0.0")
	if jerr == nil || jerr.Code != ErrCodeChecksumMismatch {
		t.Fatalf("expected checksum_mismatch, got: %+v", jerr)
	}
}

// TestChecksumAlgorithmDisplayName confirms the raw registry constant
// spellings map to their conventional display form, and that an
// unrecognized future algorithm still shows SOMETHING (uppercased)
// rather than silently disappearing.
func TestChecksumAlgorithmDisplayName(t *testing.T) {
	cases := map[string]string{
		registry.SHA256: "SHA-256",
		registry.SHA1:   "SHA-1",
		"sha512":        "SHA512", // unrecognized -- falls back to a plain uppercase of whatever it's given
	}
	for input, want := range cases {
		if got := checksumAlgorithmDisplayName(input); got != want {
			t.Errorf("checksumAlgorithmDisplayName(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestInstallCmd_TextModeShowsChecksumVerifiedLine is the real,
// end-to-end confirmation for the plain-text path specifically
// (resolveInstallJSON's own test above already covers the JSON
// payload's Checksum/ChecksumAlgorithm fields, but that's a different
// code path from the interactive RunE's fmt.Fprintln call) -- a real
// download against a real fake server, through the actual command,
// not just an inference that the same data must be right because the
// JSON path is.
func TestInstallCmd_TextModeShowsChecksumVerifiedLine(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	archive, checksum := buildFakeTarGz(t)
	server := newFakeInstallServer(t, archive)
	withTempProvider(t, "kafka", &fakeInstallProvider{
		name: "apache",
		asset: registry.Asset{
			URL:               server.URL,
			Filename:          "fake-1.0.0.tar.gz",
			Checksum:          checksum,
			ChecksumAlgorithm: registry.SHA256,
		},
	})

	var runErr error
	out := withSession(t, func() {
		cmd := newInstallCmd()
		cmd.SetContext(context.Background())
		runErr = cmd.RunE(cmd, []string{"kafka", "1.0.0"})
	})
	if runErr != nil {
		t.Fatalf("expected a successful install, got: %v", runErr)
	}
	if !strings.Contains(out, "installed") {
		t.Fatalf("expected the usual install-succeeded line, got: %q", out)
	}
	if !strings.Contains(out, "SHA-256 checksum verified: "+checksum) {
		t.Errorf("expected the checksum-verified line with the real hash, got: %q", out)
	}
}

func mustTool(t *testing.T, name string) tooldef.Tool {
	t.Helper()
	tool, ok := tooldef.Get(name)
	if !ok {
		t.Fatalf("tooldef.Get(%q) failed -- test fixture assumption broken", name)
	}
	return tool
}

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
	"testing"

	"sdkkeeper/internal/registry"
	"sdkkeeper/internal/tooldef"
)

// --- classifyInstallArg: the version_required vs vendor_required split ---

// TestClassifyInstallArg_FullIdentifierResolves confirms a complete
// version+vendor identifier (or, for a single-vendor tool, a bare
// version) needs no picker and resolves directly -- the common,
// no-ambiguity case for both shapes of tool.
func TestClassifyInstallArg_FullIdentifierResolves(t *testing.T) {
	tool, _ := javaTool()
	provider, version, jerr := classifyInstallArg(tool, "21.0.2-temurin")
	if jerr != nil {
		t.Fatalf("expected a complete identifier to resolve, got error: %+v", jerr)
	}
	if version != "21.0.2" || provider.Name() != "temurin" {
		t.Errorf("expected version=21.0.2 provider=temurin, got version=%q provider=%q", version, provider.Name())
	}

	mavenTool, _ := tooldefGet(t, "maven")
	provider, version, jerr = classifyInstallArg(mavenTool, "3.9.9")
	if jerr != nil {
		t.Fatalf("expected a bare version to resolve for single-vendor maven, got error: %+v", jerr)
	}
	if version != "3.9.9" || provider.Name() != "apache" {
		t.Errorf("expected version=3.9.9 provider=apache, got version=%q provider=%q", version, provider.Name())
	}
}

// TestClassifyInstallArg_EmptyArgIsVersionRequired confirms no
// argument at all is unconditionally version_required, matching
// use/remove's own convention for the same case.
func TestClassifyInstallArg_EmptyArgIsVersionRequired(t *testing.T) {
	tool, _ := javaTool()
	_, _, jerr := classifyInstallArg(tool, "")
	if jerr == nil || jerr.Code != ErrCodeVersionRequired {
		t.Fatalf("expected version_required for an empty arg, got: %+v", jerr)
	}
}

// TestClassifyInstallArg_BareVendorNameIsVersionRequired confirms a
// multi-vendor tool given only a vendor name (no version at all) is
// version_required, not vendor_required -- the vendor isn't the
// missing piece here.
func TestClassifyInstallArg_BareVendorNameIsVersionRequired(t *testing.T) {
	tool, _ := javaTool()
	_, _, jerr := classifyInstallArg(tool, "temurin")
	if jerr == nil || jerr.Code != ErrCodeVersionRequired {
		t.Fatalf("expected version_required for a bare vendor name, got: %+v", jerr)
	}
}

// TestClassifyInstallArg_VersionWithoutVendorIsVendorRequired is the
// central new case this conversation added: a multi-vendor tool given
// a real-looking version with no matching vendor suffix is
// vendor_required, distinct from version_required.
func TestClassifyInstallArg_VersionWithoutVendorIsVendorRequired(t *testing.T) {
	tool, _ := javaTool()
	_, _, jerr := classifyInstallArg(tool, "21.0.2")
	if jerr == nil || jerr.Code != ErrCodeVendorRequired {
		t.Fatalf("expected vendor_required for a version with no vendor suffix, got: %+v", jerr)
	}
}

// TestClassifyInstallArg_SingleVendorIncompleteVersionIsVersionRequired
// confirms a single-vendor tool (no vendor concept at all) never
// reports vendor_required -- an incomplete/bare-major arg there is
// always version_required.
func TestClassifyInstallArg_SingleVendorIncompleteVersionIsVersionRequired(t *testing.T) {
	mavenTool, _ := tooldefGet(t, "maven")
	_, _, jerr := classifyInstallArg(mavenTool, "3")
	if jerr == nil || jerr.Code != ErrCodeVersionRequired {
		t.Fatalf("expected version_required for an incomplete single-vendor version, got: %+v", jerr)
	}
}

// --- resolveInstallJSON: full end-to-end error and success paths ---

func TestResolveInstallJSON_UnknownToolIsAmbiguousTool(t *testing.T) {
	setTestHome(t, t.TempDir())
	_, jerr := resolveInstallJSON(context.Background(), "not-a-real-tool", "1.0")
	if jerr == nil || jerr.Code != ErrCodeAmbiguousTool {
		t.Fatalf("expected ambiguous_tool, got: %+v", jerr)
	}
}

// TestResolveInstallJSON_NoProviderToolIsNotFound confirms a real,
// registered tool with zero install providers (node, per
// providersByTool) is reported as not_found, mirroring the
// interactive path's own "install not yet supported" message.
func TestResolveInstallJSON_NoProviderToolIsNotFound(t *testing.T) {
	setTestHome(t, t.TempDir())
	_, jerr := resolveInstallJSON(context.Background(), "node", "1.0.0")
	if jerr == nil || jerr.Code != ErrCodeNotFound {
		t.Fatalf("expected not_found for a tool with no install providers, got: %+v", jerr)
	}
}

func TestResolveInstallJSON_NoVersionIsVersionRequired(t *testing.T) {
	setTestHome(t, t.TempDir())
	_, jerr := resolveInstallJSON(context.Background(), "java", "")
	if jerr == nil || jerr.Code != ErrCodeVersionRequired {
		t.Fatalf("expected version_required, got: %+v", jerr)
	}
}

func TestResolveInstallJSON_VersionWithoutVendorIsVendorRequired(t *testing.T) {
	setTestHome(t, t.TempDir())
	_, jerr := resolveInstallJSON(context.Background(), "java", "21.0.2")
	if jerr == nil || jerr.Code != ErrCodeVendorRequired {
		t.Fatalf("expected vendor_required, got: %+v", jerr)
	}
}

// TestResolveInstallJSON_AlreadyInstalledIsAlreadyInstalled confirms
// the pre-download Lstat guard reports the new, dedicated
// already_installed code -- not already_registered (add's own code
// for the same "slot taken" shape) and not a generic not_found.
func TestResolveInstallJSON_AlreadyInstalledIsAlreadyInstalled(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")

	_, jerr := resolveInstallJSON(context.Background(), "java", "21.0.2-temurin")
	if jerr == nil || jerr.Code != ErrCodeAlreadyInstalled {
		t.Fatalf("expected already_installed, got: %+v", jerr)
	}
}

// buildFakeTarGz creates a minimal, real, valid .tar.gz (one
// top-level directory, one file inside it) and returns its bytes and
// SHA256 checksum -- installer.Install's extractor and checksum
// verification both need a genuinely valid archive, not a stub.
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

// fakeInstallProvider is a registry.Provider stub whose ResolveAsset
// points at a real local httptest server -- so resolveInstallJSON's
// happy path is exercised through a genuine download/verify/extract/
// place cycle, not mocked away entirely.
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

// withTempProvider registers a single-vendor provider under a
// temporary tool name in providersByTool for the duration of the
// test, restoring whatever (if anything) was there before -- same
// pattern providers_test.go already uses for
// TestParseFullIdentifier_SingleVendorAcceptsBareVersion.
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

// TestResolveInstallJSON_RealDownloadInstallsAndReportsInstalled is
// the happy-path proof: a real HTTP download, real checksum
// verification, and real extraction against a temporary HOME,
// confirming the returned payload's action/path/bytes are all
// correct -- not just that no error occurred.
func TestResolveInstallJSON_RealDownloadInstallsAndReportsInstalled(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	archive, checksum := buildFakeTarGz(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer server.Close()

	withTempProvider(t, "kafka", &fakeInstallProvider{
		name: "apache",
		asset: registry.Asset{
			URL:               server.URL,
			Filename:          "fake-1.0.0.tar.gz",
			Checksum:          checksum,
			ChecksumAlgorithm: registry.SHA256,
		},
	})
	// kafka is registered in tooldef.Registry (see registry.go) but
	// has no install provider of its own yet -- fine to borrow here
	// since only its Name()/FolderPrefix/CandidateRoot are used, none
	// of which depend on real vendor behavior.

	data, jerr := resolveInstallJSON(context.Background(), "kafka", "1.0.0")
	if jerr != nil {
		t.Fatalf("expected a successful install, got error: %+v", jerr)
	}
	if data.Action != string(actionInstalled) {
		t.Errorf("expected action=installed, got %q", data.Action)
	}
	if data.Version != "1.0.0" {
		t.Errorf("expected version=1.0.0, got %q", data.Version)
	}
	if data.Vendor != nil {
		t.Errorf("expected a nil vendor for a single-provider tool, got %q", *data.Vendor)
	}
	if data.Bytes != int64(len(archive)) {
		t.Errorf("expected bytes=%d, got %d", len(archive), data.Bytes)
	}

	tool, _ := tooldefGet(t, "kafka")
	wantPath := filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+"1.0.0")
	if data.Path != wantPath {
		t.Errorf("expected path=%q, got %q", wantPath, data.Path)
	}

	// A second install of the exact same version must now report
	// already_installed, confirming the on-disk result is real, not
	// just an in-memory payload.
	_, jerr = resolveInstallJSON(context.Background(), "kafka", "1.0.0")
	if jerr == nil || jerr.Code != ErrCodeAlreadyInstalled {
		t.Fatalf("expected a repeat install to be already_installed, got: %+v", jerr)
	}
}

// TestResolveInstallJSON_ChecksumMismatchIsChecksumMismatch confirms
// a real, deliberately wrong checksum is classified as
// checksum_mismatch, not a generic activation_failed or
// internal_error.
func TestResolveInstallJSON_ChecksumMismatchIsChecksumMismatch(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	archive, _ := buildFakeTarGz(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer server.Close()

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

// javaTool and tooldefGet are small shared test lookups -- java's
// bare tooldef.Get is used often enough across this file to warrant
// a one-line wrapper matching this file's own style.
func javaTool() (tooldef.Tool, bool) {
	return tooldef.Get("java")
}

func tooldefGet(t *testing.T, name string) (tooldef.Tool, bool) {
	t.Helper()
	tool, ok := tooldef.Get(name)
	if !ok {
		t.Fatalf("tooldef.Get(%q) failed -- test fixture assumption broken", name)
	}
	return tool, ok
}

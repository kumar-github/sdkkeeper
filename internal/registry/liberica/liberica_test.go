package liberica

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"sdkkeeper/internal/registry"
)

// sampleResponse mirrors BellSoft's own documented response shape for
// /v1/liberica/releases -- a flat array, unlike Adoptium's nested
// release+binaries objects. Field names and values are based directly
// on BellSoft's own published API documentation examples (e.g. the
// "11.0.5+11" / sha1 "9956658bdc98f844bacbf451b8e8f0622544b539"
// example is taken nearly verbatim from their docs).
const sampleResponse = `[
  {
    "bitness": 64,
    "os": "macos",
    "downloadUrl": "https://github.com/bell-sw/Liberica/releases/download/11.0.5+11/bellsoft-jdk11.0.5+11-macos-amd64.tar.gz",
    "version": "11.0.5+11",
    "featureVersion": 11,
    "packageType": "tar.gz",
    "sha1": "9956658BDC98F844BACBF451B8E8F0622544B539",
    "filename": "bellsoft-jdk11.0.5+11-macos-amd64.tar.gz",
    "architecture": "x86"
  },
  {
    "bitness": 64,
    "os": "macos",
    "downloadUrl": "https://github.com/bell-sw/Liberica/releases/download/11.0.6+10/bellsoft-jdk11.0.6+10-macos-amd64.tar.gz",
    "version": "11.0.6+10",
    "featureVersion": 11,
    "packageType": "tar.gz",
    "sha1": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "filename": "bellsoft-jdk11.0.6+10-macos-amd64.tar.gz",
    "architecture": "x86"
  }
]`

// TestResolveAsset_LegacyJava8Version is a regression test for a
// real, live-reported bug: "8u504" (Liberica's own legacy Java 8
// identifier -- confirmed directly from BellSoft's own release notes,
// which state "The version number is 8" for every such release) used
// to be sent AS-IS as the version-feature query parameter, rejected
// by BellSoft's real API with "Unexpected parameter value" for
// exactly that reason. Confirms the fix by inspecting the ACTUAL
// outgoing request URL, not just majorVersion's return value in
// isolation.
func TestResolveAsset_LegacyJava8Version(t *testing.T) {
	var capturedURL *url.URL
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{
			"version": "8u504+9",
			"featureVersion": 8,
			"downloadUrl": "https://example.com/liberica-8u504.tar.gz",
			"sha1": "abcdef1234567890abcdef1234567890abcdef12",
			"filename": "liberica-8u504.tar.gz"
		}]`))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	asset, err := p.ResolveAsset(context.Background(), "8u504", "darwin", "amd64")
	if err != nil {
		t.Fatalf("ResolveAsset failed: %v", err)
	}
	if got := capturedURL.Query().Get("version-feature"); got != "8" {
		t.Errorf("expected version-feature=8, got: %q (the exact bug: this used to be \"8u504\")", got)
	}
	if asset.URL != "https://example.com/liberica-8u504.tar.gz" {
		t.Errorf("unexpected URL: %s", asset.URL)
	}
}

func TestResolveAsset_Success(t *testing.T) {
	var capturedURL *url.URL
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(sampleResponse))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	// Bare version, matching every other command in this tool -- not
	// the vendor's build-suffixed string ("11.0.5+11").
	asset, err := p.ResolveAsset(context.Background(), "11.0.5", "darwin", "amd64")
	if err != nil {
		t.Fatalf("ResolveAsset failed: %v", err)
	}

	if asset.URL != "https://github.com/bell-sw/Liberica/releases/download/11.0.5+11/bellsoft-jdk11.0.5+11-macos-amd64.tar.gz" {
		t.Errorf("unexpected URL: %s", asset.URL)
	}
	if asset.Checksum != "9956658bdc98f844bacbf451b8e8f0622544b539" {
		t.Errorf("expected checksum to be lowercased, got: %s", asset.Checksum)
	}
	if asset.ChecksumAlgorithm != registry.SHA1 {
		t.Errorf("expected ChecksumAlgorithm to be SHA1 (Liberica doesn't publish SHA-256), got: %s", asset.ChecksumAlgorithm)
	}
	if asset.Filename != "bellsoft-jdk11.0.5+11-macos-amd64.tar.gz" {
		t.Errorf("unexpected filename: %s", asset.Filename)
	}

	// Confirm OS/arch/bitness were correctly translated to Liberica's
	// own vocabulary -- a genuinely different, two-axis system from
	// Temurin's single arch string.
	if capturedURL.Query().Get("os") != "macos" {
		t.Errorf("expected os=macos, got: %s", capturedURL.Query().Get("os"))
	}
	if capturedURL.Query().Get("arch") != "x86" {
		t.Errorf("expected arch=x86, got: %s", capturedURL.Query().Get("arch"))
	}
	if capturedURL.Query().Get("bitness") != "64" {
		t.Errorf("expected bitness=64, got: %s", capturedURL.Query().Get("bitness"))
	}
	if capturedURL.Query().Get("version-feature") != "11" {
		t.Errorf("expected major version 11, got: %s", capturedURL.Query().Get("version-feature"))
	}
}

func TestResolveAsset_ArmTranslation(t *testing.T) {
	var capturedURL *url.URL
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL
		w.Write([]byte(sampleResponse))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	p.ResolveAsset(context.Background(), "11.0.5", "linux", "arm64")

	if capturedURL.Query().Get("arch") != "arm" {
		t.Errorf("expected arch=arm for arm64, got: %s", capturedURL.Query().Get("arch"))
	}
	if capturedURL.Query().Get("bitness") != "64" {
		t.Errorf("expected bitness=64, got: %s", capturedURL.Query().Get("bitness"))
	}
	if capturedURL.Query().Get("os") != "linux" {
		t.Errorf("expected os=linux, got: %s", capturedURL.Query().Get("os"))
	}
}

func TestResolveAsset_VersionNotInResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sampleResponse))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	_, err := p.ResolveAsset(context.Background(), "99.0.0", "darwin", "amd64")
	if err != registry.ErrVersionNotFound {
		t.Errorf("expected ErrVersionNotFound, got: %v", err)
	}
}

func TestResolveAsset_HTTP404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	_, err := p.ResolveAsset(context.Background(), "11.0.5", "darwin", "amd64")
	if err != registry.ErrVersionNotFound {
		t.Errorf("expected a 404 response to map to ErrVersionNotFound, got: %v", err)
	}
}

func TestResolveAsset_HTTP500(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	_, err := p.ResolveAsset(context.Background(), "11.0.5", "darwin", "amd64")
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	if err == registry.ErrVersionNotFound {
		t.Error("a 500 (server error) should NOT be reported the same as a genuine not-found")
	}
}

func TestResolveAsset_MalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("[not valid json"))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	_, err := p.ResolveAsset(context.Background(), "11.0.5", "darwin", "amd64")
	if err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestResolveAsset_UnsupportedOS(t *testing.T) {
	p := &Provider{BaseURL: "http://should-not-be-called.invalid"}
	_, err := p.ResolveAsset(context.Background(), "11.0.5", "windows", "amd64")
	if err == nil {
		t.Fatal("expected an error for an unsupported OS -- should fail before making any request")
	}
}

// TestListPatchVersions_NewestFirstNumericSort is a regression test
// for a real, live-reported bug: `sk search java liberica 25` showed
// versions in a genuinely random-looking order -- "25.0.1", "25.0.1",
// "25", "25.0.4", "25.0.4.1" in the exact order BellSoft's own API
// returned them, since ListPatchVersions never sorted at all before
// this. Reproduced here with that same scrambled order as the raw
// response.
func TestListPatchVersions_NewestFirstNumericSort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
			{"version": "25.0.1+8", "featureVersion": 25},
			{"version": "25+9", "featureVersion": 25},
			{"version": "25.0.4+1", "featureVersion": 25},
			{"version": "25.0.4.1+1", "featureVersion": 25},
			{"version": "25.0.2+1", "featureVersion": 25},
			{"version": "25.0.3+1", "featureVersion": 25}
		]`))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	versions, err := p.ListPatchVersions(context.Background(), "25", "darwin", "amd64")
	if err != nil {
		t.Fatalf("ListPatchVersions failed: %v", err)
	}

	want := []string{"25.0.4.1", "25.0.4", "25.0.3", "25.0.2", "25.0.1", "25"}
	if len(versions) != len(want) {
		t.Fatalf("expected %d versions, got %d: %v", len(want), len(versions), versions)
	}
	for i, w := range want {
		if versions[i] != w {
			t.Errorf("expected newest-first numeric order %v, got %v", want, versions)
			break
		}
	}
}

func TestListPatchVersions_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sampleResponse))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	versions, err := p.ListPatchVersions(context.Background(), "11", "darwin", "amd64")
	if err != nil {
		t.Fatalf("ListPatchVersions failed: %v", err)
	}

	want := []string{"11.0.5", "11.0.6"}
	if len(versions) != len(want) {
		t.Fatalf("expected %d versions, got %d: %v", len(want), len(versions), versions)
	}
	for _, w := range want {
		found := false
		for _, v := range versions {
			if v == w {
				found = true
			}
		}
		if !found {
			t.Errorf("expected %q in results, got: %v", w, versions)
		}
	}
}

// TestListPatchVersions_DuplicateReleasesAreDeduplicated is a
// regression test for a real, live-reported bug: `sk search java
// liberica 22` showed "22.0.1" twice, both marked "installed" --
// reproduced here directly with a synthetic response containing two
// distinct raw releases (different build numbers, e.g. a vendor-
// issued rebuild) that both strip down to the same bare version.
func TestListPatchVersions_DuplicateReleasesAreDeduplicated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
			{"version": "22.0.1+8", "featureVersion": 22},
			{"version": "22.0.1+9", "featureVersion": 22},
			{"version": "22.0.2+1", "featureVersion": 22}
		]`))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	versions, err := p.ListPatchVersions(context.Background(), "22", "darwin", "amd64")
	if err != nil {
		t.Fatalf("ListPatchVersions failed: %v", err)
	}

	count := 0
	for _, v := range versions {
		if v == "22.0.1" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected \"22.0.1\" to appear exactly once, appeared %d times in: %v", count, versions)
	}
}

func TestListPatchVersions_EmptyMajor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	_, err := p.ListPatchVersions(context.Background(), "99", "darwin", "amd64")
	if err != registry.ErrVersionNotFound {
		t.Errorf("expected ErrVersionNotFound for a major with no releases, got: %v", err)
	}
}

// TestListMajorVersionsWithLTS_NoVersionModifierParam is a regression
// test for a real, live-reported bug: this query used to send
// version-modifier=latest with no version-feature filter -- every
// real-world use of that parameter (BellSoft's own docs, and multiple
// independent third-party package-manager manifests) pairs it with a
// SPECIFIC major version, to mean "the one latest patch within THIS
// major" -- sent bare, BellSoft correctly returns just the single,
// globally newest release across ALL majors, which is exactly why
// only one major version (the current newest) ever appeared in the
// picker. The mock server in TestListMajorVersionsWithLTS_Success
// doesn't actually inspect query params at all (it always returns a
// rich, multi-major fixture regardless of what's sent), so it could
// never have caught this on its own -- this test asserts on the
// ACTUAL request URL instead, the same pattern used to regression-test
// Temurin's own real, live-reported version-parsing bug.
func TestListMajorVersionsWithLTS_NoVersionModifierParam(t *testing.T) {
	var capturedURL *url.URL
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL
		w.Write([]byte(`[{"version": "21.0.2+13", "featureVersion": 21, "LTS": true}]`))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	if _, err := p.ListMajorVersionsWithLTS(context.Background()); err != nil {
		t.Fatalf("ListMajorVersionsWithLTS failed: %v", err)
	}

	if capturedURL.Query().Get("version-modifier") != "" {
		t.Errorf("expected NO version-modifier param, got: %q (full query: %s)", capturedURL.Query().Get("version-modifier"), capturedURL.RawQuery)
	}
}

func TestListMajorVersionsWithLTS_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
			{"version": "21.0.2+13", "featureVersion": 21, "LTS": true},
			{"version": "17.0.9+9", "featureVersion": 17, "LTS": true},
			{"version": "21.0.1+12", "featureVersion": 21, "LTS": true},
			{"version": "22.0.1+8", "featureVersion": 22, "LTS": false}
		]`))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	infos, err := p.ListMajorVersionsWithLTS(context.Background())
	if err != nil {
		t.Fatalf("ListMajorVersionsWithLTS failed: %v", err)
	}

	want := map[string]bool{"22": false, "21": true, "17": true}
	if len(infos) != len(want) {
		t.Fatalf("expected %d unique majors, got %d: %+v", len(want), len(infos), infos)
	}
	for _, info := range infos {
		wantLTS, ok := want[info.Number]
		if !ok {
			t.Errorf("unexpected version in results: %q", info.Number)
			continue
		}
		if info.LTS != wantLTS {
			t.Errorf("expected %q LTS=%v, got %v", info.Number, wantLTS, info.LTS)
		}
	}
}

func TestListMajorVersions_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
			{"version": "21.0.2+13", "featureVersion": 21},
			{"version": "17.0.9+9", "featureVersion": 17},
			{"version": "21.0.1+12", "featureVersion": 21},
			{"version": "11.0.22+12", "featureVersion": 11}
		]`))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	versions, err := p.ListMajorVersions(context.Background())
	if err != nil {
		t.Fatalf("ListMajorVersions failed: %v", err)
	}

	want := []string{"21", "17", "11"}
	if len(versions) != len(want) {
		t.Fatalf("expected %d unique majors, got %d: %v", len(want), len(versions), versions)
	}
	for i, v := range versions {
		if v != want[i] {
			t.Errorf("expected newest-first order %v, got %v", want, versions)
			break
		}
	}
}

func TestStripBuildMetadata(t *testing.T) {
	cases := map[string]string{
		"11.0.5+11": "11.0.5",
		"11.0.5":    "11.0.5",
		"21.0.2+13": "21.0.2",
	}
	for in, want := range cases {
		got := stripBuildMetadata(in)
		if got != want {
			t.Errorf("stripBuildMetadata(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMajorVersion(t *testing.T) {
	cases := map[string]string{
		"11.0.5+11": "11",
		"11.0.5":    "11",
		"8":         "8",
		"8u504":     "8", // legacy Liberica Java 8 scheme -- real, live bug
		"8u452":     "8",
		"7u491":     "7",
	}
	for in, want := range cases {
		got, err := majorVersion(in)
		if err != nil {
			t.Errorf("majorVersion(%q) returned error: %v", in, err)
		}
		if got != want {
			t.Errorf("majorVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsAllDigits(t *testing.T) {
	trueCases := []string{"8", "504", "0"}
	for _, s := range trueCases {
		if !isAllDigits(s) {
			t.Errorf("isAllDigits(%q) = false, want true", s)
		}
	}
	falseCases := []string{"", "8u", "u8", "8.0", "-8", "8 "}
	for _, s := range falseCases {
		if isAllDigits(s) {
			t.Errorf("isAllDigits(%q) = true, want false", s)
		}
	}
}

// TestLibericaOS closes a real, pre-existing gap: libericaOS had no
// DIRECT test at all before this -- only indirect coverage via
// ResolveAsset's own darwin/linux end-to-end tests. Table-driven so
// the new "windows" case (added for Windows support) sits alongside
// every previously-only-indirectly-tested case, all verified the
// same, explicit way.
func TestLibericaOS(t *testing.T) {
	cases := map[string]string{
		"darwin":  "macos",
		"linux":   "linux",
		"windows": "windows",
	}
	for goos, want := range cases {
		got, err := libericaOS(goos)
		if err != nil {
			t.Errorf("libericaOS(%q) returned error: %v", goos, err)
		}
		if got != want {
			t.Errorf("libericaOS(%q) = %q, want %q", goos, got, want)
		}
	}
}

func TestLibericaOS_UnsupportedReturnsError(t *testing.T) {
	if _, err := libericaOS("plan9"); err == nil {
		t.Error("expected an error for an unsupported OS")
	}
}

// TestLibericaArch closes the same kind of gap as TestLibericaOS, for
// the two-axis arch+bitness side.
func TestLibericaArch(t *testing.T) {
	type want struct {
		arch    string
		bitness int
	}
	cases := map[string]want{
		"amd64": {"x86", 64},
		"arm64": {"arm", 64},
	}
	for goarch, w := range cases {
		arch, bitness, err := libericaArch(goarch)
		if err != nil {
			t.Errorf("libericaArch(%q) returned error: %v", goarch, err)
		}
		if arch != w.arch || bitness != w.bitness {
			t.Errorf("libericaArch(%q) = (%q, %d), want (%q, %d)", goarch, arch, bitness, w.arch, w.bitness)
		}
	}
}

func TestLibericaArch_UnsupportedReturnsError(t *testing.T) {
	if _, _, err := libericaArch("riscv64"); err == nil {
		t.Error("expected an error for an unsupported architecture")
	}
}

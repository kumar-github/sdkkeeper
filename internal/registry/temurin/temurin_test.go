package temurin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"sdkkeeper/internal/registry"
)

// sampleResponse mirrors the REAL, live Adoptium feature_releases
// response shape. The top-level key is "version_data", NOT "version"
// -- an earlier version of this fixture (and the matching code) used
// "version", which was never independently checked against a live
// call (this sandbox's network allowlist doesn't reach
// api.adoptium.net) and turned out to be wrong: since the JSON key
// never matched, OpenJDKVersion/Semver silently deserialized as empty
// strings, meaning NO version could ever match, regardless of what
// was requested -- a real bug that made every `sk install` fail. This
// fixture (and every value in it) now matches an ACTUAL response,
// retrieved directly by the user via curl+jq against the real API.
const sampleResponse = `[
  {
    "version_data": {
      "openjdk_version": "21.0.1+12",
      "semver": "21.0.1+12"
    },
    "binaries": [
      {
        "package": {
          "name": "OpenJDK21U-jdk_x64_mac_hotspot_21.0.1_12.tar.gz",
          "link": "https://example.com/OpenJDK21U-jdk_x64_mac_hotspot_21.0.1_12.tar.gz",
          "checksum": "AABBCCDDEEFF00112233445566778899AABBCCDDEEFF00112233445566778899"
        }
      }
    ]
  },
  {
    "version_data": {
      "openjdk_version": "21.0.2+13",
      "semver": "21.0.2+13"
    },
    "binaries": [
      {
        "package": {
          "name": "OpenJDK21U-jdk_x64_mac_hotspot_21.0.2_13.tar.gz",
          "link": "https://example.com/OpenJDK21U-jdk_x64_mac_hotspot_21.0.2_13.tar.gz",
          "checksum": "1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890ABCD"
        }
      }
    ]
  },
  {
    "version_data": {
      "build": 1,
      "major": 21,
      "minor": 0,
      "openjdk_version": "21.0.12.1+1-LTS",
      "optional": "LTS",
      "patch": 1,
      "security": 12,
      "semver": "21.0.12+101.0.LTS"
    },
    "binaries": [
      {
        "package": {
          "name": "OpenJDK21U-jdk_x64_mac_hotspot_21.0.12.1_1.tar.gz",
          "link": "https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.12.1%2B1/OpenJDK21U-jdk_x64_mac_hotspot_21.0.12.1_1.tar.gz",
          "checksum": "44db0f08196daf19a47f90d13388b0c943b67663cb537f998fe29e836fa842ce"
        }
      }
    ]
  }
]`

func TestResolveAsset_Success(t *testing.T) {
	var capturedURL *url.URL
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(sampleResponse))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	// Real users type the bare version everywhere else in this tool
	// (e.g. "sk use java 21.0.2") -- NOT the vendor's build-suffixed
	// string ("21.0.2+13") that the API itself actually returns. This
	// is the realistic case, and the one that was initially broken:
	// see stripBuildMetadata's doc comment.
	asset, err := p.ResolveAsset(context.Background(), "21.0.2", "darwin", "amd64")
	if err != nil {
		t.Fatalf("ResolveAsset failed: %v", err)
	}

	if asset.URL != "https://example.com/OpenJDK21U-jdk_x64_mac_hotspot_21.0.2_13.tar.gz" {
		t.Errorf("unexpected URL: %s", asset.URL)
	}
	if asset.Checksum != "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcd" {
		t.Errorf("expected checksum to be lowercased, got: %s", asset.Checksum)
	}
	if asset.Filename != "OpenJDK21U-jdk_x64_mac_hotspot_21.0.2_13.tar.gz" {
		t.Errorf("unexpected filename: %s", asset.Filename)
	}

	// Confirm OS/arch were correctly translated to Adoptium's own
	// vocabulary in the actual outgoing request, not Go's runtime names.
	if capturedURL.Query().Get("os") != "mac" {
		t.Errorf("expected os=mac in request, got: %s", capturedURL.Query().Get("os"))
	}
	if capturedURL.Query().Get("architecture") != "x64" {
		t.Errorf("expected architecture=x64 in request, got: %s", capturedURL.Query().Get("architecture"))
	}
	// Regression test for a real, confirmed bug: without an explicit,
	// generous page_size, the API's small default page (confirmed
	// live to be 10) silently excludes older patches of a major
	// version, making a completely valid, installable version
	// (e.g. 21.0.2, once superseded by 13+ newer releases) report as
	// "not found". Confirmed fixed against the REAL live API by the
	// user directly (not just this local fake server).
	if capturedURL.Query().Get("page_size") == "" {
		t.Error("expected an explicit page_size in the request -- without it, older patch versions silently fall outside the API's small default page")
	}
	if !strings.Contains(capturedURL.Path, "/v3/assets/feature_releases/21/ga") {
		t.Errorf("expected major version 21 in path, got: %s", capturedURL.Path)
	}
}

// TestResolveAsset_LegacyJava8Version is a regression test for a
// real, live bug: the patch picker correctly listed "1.8.0_482-b08"
// (Adoptium's own openjdk_version string for legacy Java 8 releases),
// but ResolveAsset queried major "1" instead of "8" -- "1" doesn't
// exist, so install always failed with "not found" for any Java 8
// release, even though the version genuinely exists.
func TestResolveAsset_LegacyJava8Version(t *testing.T) {
	var capturedURL *url.URL
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{
			"version_data": {"openjdk_version": "1.8.0_482-b08", "semver": "8.0.482+8"},
			"binaries": [{"package": {
				"name": "OpenJDK8U-jdk_x64_mac_hotspot_8u482b08.tar.gz",
				"link": "https://example.com/OpenJDK8U-jdk_x64_mac_hotspot_8u482b08.tar.gz",
				"checksum": "abcdef1234567890abcdef1234567890abcdef1234567890abcdef12345678"
			}}]
		}]`))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	asset, err := p.ResolveAsset(context.Background(), "1.8.0_482-b08", "darwin", "amd64")
	if err != nil {
		t.Fatalf("ResolveAsset failed: %v", err)
	}
	if !strings.Contains(capturedURL.Path, "/v3/assets/feature_releases/8/ga") {
		t.Errorf("expected major version 8 in path, got: %s", capturedURL.Path)
	}
	if asset.URL != "https://example.com/OpenJDK8U-jdk_x64_mac_hotspot_8u482b08.tar.gz" {
		t.Errorf("unexpected URL: %s", asset.URL)
	}
}

// TestResolveAsset_FourComponentVersion uses the EXACT real response
// data retrieved by the user for JDK 21.0.12.1+1-LTS -- a version with
// an unusual 4th ("patch") component, which is exactly the case where
// stripBuildMetadata's doc comment notes semver and openjdk_version
// diverge after stripping. Confirms matching against a real user-typed
// 4-component version ("21.0.12.1") still works correctly via
// openjdk_version, despite that divergence.
func TestResolveAsset_FourComponentVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sampleResponse))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	asset, err := p.ResolveAsset(context.Background(), "21.0.12.1", "darwin", "amd64")
	if err != nil {
		t.Fatalf("ResolveAsset failed for a real 4-component version: %v", err)
	}
	if !strings.Contains(asset.URL, "21.0.12.1_1.tar.gz") {
		t.Errorf("unexpected URL for 4-component version: %s", asset.URL)
	}
}

// TestListPatchVersions_NewestFirstNumericSort is a regression test
// guarding against the same class of bug Liberica's own
// ListPatchVersions had (see that provider's identical test): this
// used to rely on an unverified assumption that Adoptium's API always
// returns results already sorted, rather than sorting explicitly.
// Reproduced with a deliberately scrambled response order.
func TestListPatchVersions_NewestFirstNumericSort(t *testing.T) {
	scrambled := `[
		{"version_data": {"openjdk_version": "21.0.2+13"}, "binaries": [{"package": {"name": "a.tar.gz", "link": "https://example.com/a.tar.gz", "checksum": "AA"}}]},
		{"version_data": {"openjdk_version": "21"}, "binaries": [{"package": {"name": "b.tar.gz", "link": "https://example.com/b.tar.gz", "checksum": "BB"}}]},
		{"version_data": {"openjdk_version": "21.0.12.1+12"}, "binaries": [{"package": {"name": "c.tar.gz", "link": "https://example.com/c.tar.gz", "checksum": "CC"}}]},
		{"version_data": {"openjdk_version": "21.0.1+12"}, "binaries": [{"package": {"name": "d.tar.gz", "link": "https://example.com/d.tar.gz", "checksum": "DD"}}]}
	]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(scrambled))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	versions, err := p.ListPatchVersions(context.Background(), "21", "darwin", "amd64")
	if err != nil {
		t.Fatalf("ListPatchVersions failed: %v", err)
	}

	want := []string{"21.0.12.1", "21.0.2", "21.0.1", "21"}
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
	versions, err := p.ListPatchVersions(context.Background(), "21", "darwin", "amd64")
	if err != nil {
		t.Fatalf("ListPatchVersions failed: %v", err)
	}

	want := []string{"21.0.1", "21.0.2", "21.0.12.1"}
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

func TestListMajorVersionsWithLTS_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Same real fixture as TestListMajorVersions_Success -- 8, 11,
		// 17, 21 are LTS; 24, 25 are not.
		w.Write([]byte(`{
			"available_lts_releases": [8, 11, 17, 21],
			"available_releases": [8, 11, 17, 21, 24, 25],
			"most_recent_feature_release": 25,
			"most_recent_lts": 21,
			"tip_version": 26
		}`))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	infos, err := p.ListMajorVersionsWithLTS(context.Background())
	if err != nil {
		t.Fatalf("ListMajorVersionsWithLTS failed: %v", err)
	}

	want := map[string]bool{
		"25": false, "24": false, "21": true, "17": true, "11": true, "8": true,
	}
	if len(infos) != len(want) {
		t.Fatalf("expected %d entries, got %d: %+v", len(want), len(infos), infos)
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

	// Newest-first order preserved, matching ListMajorVersions's own
	// existing guarantee.
	wantOrder := []string{"25", "24", "21", "17", "11", "8"}
	for i, info := range infos {
		if info.Number != wantOrder[i] {
			t.Errorf("expected newest-first order %v, got position %d = %q", wantOrder, i, info.Number)
			break
		}
	}
}

func TestListMajorVersions_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Real shape confirmed from Adoptium's own cookbook example.
		w.Write([]byte(`{
			"available_lts_releases": [8, 11, 17, 21],
			"available_releases": [8, 11, 17, 21, 24, 25],
			"most_recent_feature_release": 25,
			"most_recent_lts": 21,
			"tip_version": 26
		}`))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	versions, err := p.ListMajorVersions(context.Background())
	if err != nil {
		t.Fatalf("ListMajorVersions failed: %v", err)
	}

	want := []string{"25", "24", "21", "17", "11", "8"}
	if len(versions) != len(want) {
		t.Fatalf("expected %d versions, got %d: %v", len(want), len(versions), versions)
	}
	for i, v := range versions {
		if v != want[i] {
			t.Errorf("expected newest-first order %v, got %v", want, versions)
			break
		}
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
	// version won't match anything in sampleResponse -- only checking
	// the request translation here, not the (expected) not-found result.
	p.ResolveAsset(context.Background(), "21.0.2", "linux", "arm64")

	if capturedURL.Query().Get("architecture") != "aarch64" {
		t.Errorf("expected architecture=aarch64 for arm64, got: %s", capturedURL.Query().Get("architecture"))
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
	_, err := p.ResolveAsset(context.Background(), "21.0.2", "darwin", "amd64")
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
	_, err := p.ResolveAsset(context.Background(), "21.0.2", "darwin", "amd64")
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	if err == registry.ErrVersionNotFound {
		t.Error("a 500 (server error) should NOT be reported the same as a genuine not-found -- they mean different things to a caller")
	}
}

func TestResolveAsset_MalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("{not valid json"))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	_, err := p.ResolveAsset(context.Background(), "21.0.2", "darwin", "amd64")
	if err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestResolveAsset_UnsupportedOS(t *testing.T) {
	p := &Provider{BaseURL: "http://should-not-be-called.invalid"}
	_, err := p.ResolveAsset(context.Background(), "21.0.2+13", "windows", "amd64")
	if err == nil {
		t.Fatal("expected an error for an unsupported OS -- should fail before making any request")
	}
}

func TestStripBuildMetadata(t *testing.T) {
	cases := map[string]string{
		"21.0.2+13":       "21.0.2",
		"21.0.2":          "21.0.2",
		"8+372":           "8",
		"21.0.12.1+1-LTS": "21.0.12.1", // the real, confirmed 4-component case
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
		"21.0.2+13":     "21",
		"21.0.2":        "21",
		"8":             "8",
		"1.8.0_482-b08": "8", // legacy Java 8 scheme -- real, live bug
		"1.8.0_482":     "8",
		"1.7.0_80-b15":  "7",
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

// TestAdoptiumOS closes a real, pre-existing gap: adoptiumOS had no
// DIRECT test at all before this -- only indirect coverage via
// ResolveAsset's own darwin/linux end-to-end tests. Table-driven so
// the new "windows" case (added for Windows support) sits alongside
// every previously-only-indirectly-tested case, all verified the
// same, explicit way.
func TestAdoptiumOS(t *testing.T) {
	cases := map[string]string{
		"darwin":  "mac",
		"linux":   "linux",
		"windows": "windows",
	}
	for goos, want := range cases {
		got, err := adoptiumOS(goos)
		if err != nil {
			t.Errorf("adoptiumOS(%q) returned error: %v", goos, err)
		}
		if got != want {
			t.Errorf("adoptiumOS(%q) = %q, want %q", goos, got, want)
		}
	}
}

func TestAdoptiumOS_UnsupportedReturnsError(t *testing.T) {
	if _, err := adoptiumOS("plan9"); err == nil {
		t.Error("expected an error for an unsupported OS")
	}
}

// TestAdoptiumArch closes the same kind of gap as TestAdoptiumOS,
// for the arch side.
func TestAdoptiumArch(t *testing.T) {
	cases := map[string]string{
		"amd64": "x64",
		"arm64": "aarch64",
	}
	for goarch, want := range cases {
		got, err := adoptiumArch(goarch)
		if err != nil {
			t.Errorf("adoptiumArch(%q) returned error: %v", goarch, err)
		}
		if got != want {
			t.Errorf("adoptiumArch(%q) = %q, want %q", goarch, got, want)
		}
	}
}

func TestAdoptiumArch_UnsupportedReturnsError(t *testing.T) {
	if _, err := adoptiumArch("riscv64"); err == nil {
		t.Error("expected an error for an unsupported architecture")
	}
}

package apache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sdkkeeper/internal/registry"
)

// sampleMetadata mirrors Maven's own real, documented artifact-level
// metadata schema -- confirmed directly by fetching a real, live
// maven-metadata.xml from repo1.maven.org during design research
// (a different artifact's file, but the same <metadata><versioning>
// schema Maven's own official docs describe as standard for ALL
// artifacts, apache-maven included). Includes real pre-release
// version strings (deliberately, to test filtering) alongside stable
// ones.
const sampleMetadata = `<?xml version="1.0" encoding="UTF-8"?>
<metadata>
  <groupId>org.apache.maven</groupId>
  <artifactId>apache-maven</artifactId>
  <versioning>
    <latest>3.9.9</latest>
    <release>3.9.9</release>
    <versions>
      <version>3.6.3</version>
      <version>3.8.1</version>
      <version>3.9.0-alpha-1</version>
      <version>3.9.0-beta-1</version>
      <version>3.9.0</version>
      <version>3.9.9</version>
      <version>4.0.0-beta-3</version>
      <version>4.0.0-SNAPSHOT</version>
    </versions>
    <lastUpdated>20240814085100</lastUpdated>
  </versioning>
</metadata>`

func TestFetchAllVersions_FiltersPrereleases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sampleMetadata))
	}))
	defer server.Close()

	p := &Provider{MetadataBaseURL: server.URL}
	versions, err := p.fetchAllVersions(context.Background())
	if err != nil {
		t.Fatalf("fetchAllVersions failed: %v", err)
	}

	want := map[string]bool{"3.6.3": true, "3.8.1": true, "3.9.0": true, "3.9.9": true}
	if len(versions) != len(want) {
		t.Fatalf("expected %d stable versions, got %d: %v", len(want), len(versions), versions)
	}
	for _, v := range versions {
		if !want[v] {
			t.Errorf("expected only stable releases, but found pre-release/dev version: %q", v)
		}
	}
}

func TestListMajorVersions_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sampleMetadata))
	}))
	defer server.Close()

	p := &Provider{MetadataBaseURL: server.URL}
	majors, err := p.ListMajorVersions(context.Background())
	if err != nil {
		t.Fatalf("ListMajorVersions failed: %v", err)
	}

	// Stable versions are 3.6.3, 3.8.1, 3.9.0, 3.9.9 -- all major "3".
	// The 4.0.0 entries are both pre-release/dev and correctly
	// filtered out, so "4" should NOT appear.
	if len(majors) != 1 || majors[0] != "3" {
		t.Errorf("expected exactly major '3' (4.x entries are all pre-release), got: %v", majors)
	}
}

func TestListMajorVersionsWithLTS_AlwaysFalse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sampleMetadata))
	}))
	defer server.Close()

	p := &Provider{MetadataBaseURL: server.URL}
	infos, err := p.ListMajorVersionsWithLTS(context.Background())
	if err != nil {
		t.Fatalf("ListMajorVersionsWithLTS failed: %v", err)
	}
	for _, info := range infos {
		if info.LTS {
			t.Errorf("expected LTS=false always (Maven has no LTS concept), got LTS=true for %q", info.Number)
		}
	}
}

func TestListPatchVersions_NewestFirstNumericSort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sampleMetadata))
	}))
	defer server.Close()

	p := &Provider{MetadataBaseURL: server.URL}
	patches, err := p.ListPatchVersions(context.Background(), "3", "linux", "amd64")
	if err != nil {
		t.Fatalf("ListPatchVersions failed: %v", err)
	}

	want := []string{"3.9.9", "3.9.0", "3.8.1", "3.6.3"}
	if len(patches) != len(want) {
		t.Fatalf("expected %d patches, got %d: %v", len(want), len(patches), patches)
	}
	for i, w := range want {
		if patches[i] != w {
			t.Errorf("expected newest-first numeric order %v, got %v", want, patches)
			break
		}
	}
}

func TestListPatchVersions_UnknownMajor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sampleMetadata))
	}))
	defer server.Close()

	p := &Provider{MetadataBaseURL: server.URL}
	_, err := p.ListPatchVersions(context.Background(), "99", "linux", "amd64")
	if err != registry.ErrVersionNotFound {
		t.Errorf("expected ErrVersionNotFound for a major with no releases, got: %v", err)
	}
}

// TestCompareVersions_HandlesDoubleDigitsCorrectly is a regression
// test for a real, easy-to-get-wrong bug: plain string comparison
// would put "3.10.0" BEFORE "3.9.9" (since '1' < '9' as characters),
// which is numerically backwards.
func TestCompareVersions_HandlesDoubleDigitsCorrectly(t *testing.T) {
	if compareVersions("3.10.0", "3.9.9") <= 0 {
		t.Error("expected 3.10.0 > 3.9.9 numerically, string comparison would get this backwards")
	}
	if compareVersions("3.9.9", "3.9.9") != 0 {
		t.Error("expected equal versions to compare as 0")
	}
	if compareVersions("3.9.0", "3.9.9") >= 0 {
		t.Error("expected 3.9.0 < 3.9.9")
	}
}

func TestIsStableRelease(t *testing.T) {
	cases := map[string]bool{
		"3.9.9":           true,
		"3.6.3":           true,
		"3.9.0-alpha-1":   false,
		"3.9.0-beta-1":    false,
		"4.0.0-beta-3":    false,
		"4.0.0-SNAPSHOT":  false,
		"3.0-alpha-3-RC1": false,
	}
	for version, want := range cases {
		got := isStableRelease(version)
		if got != want {
			t.Errorf("isStableRelease(%q) = %v, want %v", version, got, want)
		}
	}
}

// TestResolveAsset_Success uses a real, confirmed checksum-file
// content shape: hex digest followed by a space and the filename,
// exactly as shown in a real Apache JIRA bug report (MNGSITE-378)
// during design research -- confirming the parsing handles this
// shape correctly, not just a bare hex string.
func TestResolveAsset_Success(t *testing.T) {
	const fakeChecksum = "4bb0e0bb1fb74f1b990ba9a6493cc6345873d9188fc7613df16ab0d5bd2017de5a3917af4502792f0bad1fcc95785dcc6660f7add53548e0ec4bfb30ce4b1da7"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha512") {
			// Real shape confirmed via MNGSITE-378: hex digest + space + filename.
			w.Write([]byte(fakeChecksum + " apache-maven-3.9.9-bin.zip\n"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	p := &Provider{ArchiveBaseURL: server.URL}
	asset, err := p.ResolveAsset(context.Background(), "3.9.9", "linux", "amd64")
	if err != nil {
		t.Fatalf("ResolveAsset failed: %v", err)
	}

	if asset.Filename != "apache-maven-3.9.9-bin.zip" {
		t.Errorf("unexpected filename: %s", asset.Filename)
	}
	if asset.Checksum != fakeChecksum {
		t.Errorf("expected checksum parsed correctly from 'hex + filename' shape, got: %q", asset.Checksum)
	}
	if asset.ChecksumAlgorithm != registry.SHA512 {
		t.Errorf("expected SHA512, got: %s", asset.ChecksumAlgorithm)
	}
	wantURL := server.URL + "/maven-3/3.9.9/binaries/apache-maven-3.9.9-bin.zip"
	if asset.URL != wantURL {
		t.Errorf("expected URL %q, got %q", wantURL, asset.URL)
	}
}

// TestResolveAsset_BareChecksumShape confirms parsing ALSO handles a
// bare hex digest with no trailing filename -- both shapes were
// observed across different Maven hosting locations during research,
// and the parser must handle either correctly.
func TestResolveAsset_BareChecksumShape(t *testing.T) {
	const fakeChecksum = "d941423d115cd021514bfd06c453658b1b3e39e6240969caf4315ab7119a77299713f14b620fb2571a264f8dff2473d8af3cb47b05acf0036fc2553199a5c1ee"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha512") {
			w.Write([]byte(fakeChecksum)) // bare, no filename, no trailing newline
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	p := &Provider{ArchiveBaseURL: server.URL}
	asset, err := p.ResolveAsset(context.Background(), "3.9.5", "darwin", "arm64")
	if err != nil {
		t.Fatalf("ResolveAsset failed: %v", err)
	}
	if asset.Checksum != fakeChecksum {
		t.Errorf("expected bare checksum shape to parse correctly, got: %q", asset.Checksum)
	}
}

func TestResolveAsset_ChecksumNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	p := &Provider{ArchiveBaseURL: server.URL}
	_, err := p.ResolveAsset(context.Background(), "3.0.1", "linux", "amd64")
	if err != registry.ErrVersionNotFound {
		t.Errorf("expected ErrVersionNotFound for a version with no published checksum, got: %v", err)
	}
}

// TestResolveAsset_IgnoresOSAndArch confirms the SAME archive URL is
// returned regardless of osName/arch -- Maven publishes exactly one
// universal, cross-platform archive per version, unlike a JDK.
func TestResolveAsset_IgnoresOSAndArch(t *testing.T) {
	const fakeChecksum = "abc123"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fakeChecksum))
	}))
	defer server.Close()

	p := &Provider{ArchiveBaseURL: server.URL}
	linuxAsset, err := p.ResolveAsset(context.Background(), "3.9.9", "linux", "amd64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	windowsAsset, err := p.ResolveAsset(context.Background(), "3.9.9", "windows", "arm64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if linuxAsset.URL != windowsAsset.URL || linuxAsset.Filename != windowsAsset.Filename {
		t.Errorf("expected identical asset regardless of OS/arch, got %+v vs %+v", linuxAsset, windowsAsset)
	}
}

func TestMajorOf(t *testing.T) {
	cases := map[string]string{
		"3.9.9": "3",
		"3.6.3": "3",
		"4.0.0": "4",
		"3":     "3",
	}
	for in, want := range cases {
		got := majorOf(in)
		if got != want {
			t.Errorf("majorOf(%q) = %q, want %q", in, got, want)
		}
	}
}

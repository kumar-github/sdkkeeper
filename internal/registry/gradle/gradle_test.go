package gradle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"sdkkeeper/internal/registry"
)

// sampleVersions mirrors the REAL, confirmed JSON shape returned by
// services.gradle.org/versions/all -- fetched live during design
// research. Includes real stable releases alongside real RC/
// milestone/snapshot entries (deliberately, to test the "final"
// filter), matching actual entries observed in the live response.
const sampleVersions = `[
  {
    "version": "9.8.0-20260905023534+0000",
    "current": false,
    "snapshot": true,
    "nightly": false,
    "downloadUrl": "https://services.gradle.org/distributions-snapshots/gradle-9.8.0-20260905023534+0000-bin.zip",
    "checksumUrl": "https://services.gradle.org/distributions-snapshots/gradle-9.8.0-20260905023534+0000-bin.zip.sha256",
    "checksum": "98a74620703a4ff671a893b460b5b54c6e1586df04bf53eed25a2c4f1f6de21a",
    "final": false
  },
  {
    "version": "9.8.0-milestone-2",
    "current": false,
    "snapshot": false,
    "nightly": false,
    "downloadUrl": "https://services.gradle.org/distributions/gradle-9.8.0-milestone-2-bin.zip",
    "checksumUrl": "https://services.gradle.org/distributions/gradle-9.8.0-milestone-2-bin.zip.sha256",
    "checksum": "988558592a6377d54d5730e7ee3032fcef421c4a40022d4d3605a6ac791b1087",
    "final": false
  },
  {
    "version": "9.7.1",
    "current": true,
    "snapshot": false,
    "nightly": false,
    "downloadUrl": "https://services.gradle.org/distributions/gradle-9.7.1-bin.zip",
    "checksumUrl": "https://services.gradle.org/distributions/gradle-9.7.1-bin.zip.sha256",
    "checksum": "acd53f1edaf02f1a8ff99879f8a34b302661a057d9b063ae9e35b552f804d20a",
    "final": true
  },
  {
    "version": "9.7.0-rc-3",
    "current": false,
    "snapshot": false,
    "nightly": false,
    "downloadUrl": "https://services.gradle.org/distributions/gradle-9.7.0-rc-3-bin.zip",
    "checksumUrl": "https://services.gradle.org/distributions/gradle-9.7.0-rc-3-bin.zip.sha256",
    "checksum": "b110d83f81b724c167de7d3df42f323271d55c2edf8b6a842ce0dd20f0764331",
    "final": false
  },
  {
    "version": "9.7.0",
    "current": false,
    "snapshot": false,
    "nightly": false,
    "downloadUrl": "https://services.gradle.org/distributions/gradle-9.7.0-bin.zip",
    "checksumUrl": "https://services.gradle.org/distributions/gradle-9.7.0-bin.zip.sha256",
    "checksum": "84fbba45c7f4c64abc77460e1c00f541e9f960e3c7ed2538f1ede19eacd873ae",
    "final": true
  },
  {
    "version": "8.14.5",
    "current": false,
    "snapshot": false,
    "nightly": false,
    "downloadUrl": "https://services.gradle.org/distributions/gradle-8.14.5-bin.zip",
    "checksumUrl": "https://services.gradle.org/distributions/gradle-8.14.5-bin.zip.sha256",
    "checksum": "6f74b601422d6d6fc4e1f9a1ab6522f642c2fdcbc15ae33ebd30ba3d7198e854",
    "final": true
  },
  {
    "version": "7.6.6",
    "current": false,
    "snapshot": false,
    "nightly": false,
    "downloadUrl": "https://services.gradle.org/distributions/gradle-7.6.6-bin.zip",
    "checksumUrl": "https://services.gradle.org/distributions/gradle-7.6.6-bin.zip.sha256",
    "checksum": "673d9776f303bc7048fc3329d232d6ebf1051b07893bd9d11616fad9a8673be0",
    "final": true
  }
]`

func TestFetchAllVersions_FiltersToFinalOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sampleVersions))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	releases, err := p.fetchAllVersions(context.Background())
	if err != nil {
		t.Fatalf("fetchAllVersions failed: %v", err)
	}

	want := map[string]bool{"9.7.1": true, "9.7.0": true, "8.14.5": true, "7.6.6": true}
	if len(releases) != len(want) {
		t.Fatalf("expected %d final releases, got %d", len(want), len(releases))
	}
	for _, r := range releases {
		if !want[r.Version] {
			t.Errorf("expected only final=true releases, but found: %q", r.Version)
		}
	}
}

func TestListMajorVersions_NewestFirst(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sampleVersions))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	majors, err := p.ListMajorVersions(context.Background())
	if err != nil {
		t.Fatalf("ListMajorVersions failed: %v", err)
	}

	want := []string{"9", "8", "7"}
	if len(majors) != len(want) {
		t.Fatalf("expected %v, got %v", want, majors)
	}
	for i, w := range want {
		if majors[i] != w {
			t.Errorf("expected newest-first order %v, got %v", want, majors)
			break
		}
	}
}

func TestListMajorVersionsWithLTS_AlwaysFalse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sampleVersions))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	infos, err := p.ListMajorVersionsWithLTS(context.Background())
	if err != nil {
		t.Fatalf("ListMajorVersionsWithLTS failed: %v", err)
	}
	for _, info := range infos {
		if info.LTS {
			t.Errorf("expected LTS=false always (Gradle has no LTS concept), got LTS=true for %q", info.Number)
		}
	}
}

func TestListPatchVersions_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sampleVersions))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	patches, err := p.ListPatchVersions(context.Background(), "9", "linux", "amd64")
	if err != nil {
		t.Fatalf("ListPatchVersions failed: %v", err)
	}

	want := []string{"9.7.1", "9.7.0"}
	if len(patches) != len(want) {
		t.Fatalf("expected %v, got %v", want, patches)
	}
	for i, w := range want {
		if patches[i] != w {
			t.Errorf("expected newest-first order %v, got %v", want, patches)
			break
		}
	}
}

func TestListPatchVersions_UnknownMajor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sampleVersions))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	_, err := p.ListPatchVersions(context.Background(), "99", "linux", "amd64")
	if err != registry.ErrVersionNotFound {
		t.Errorf("expected ErrVersionNotFound, got: %v", err)
	}
}

// TestResolveAsset_Success confirms the checksum is read DIRECTLY from
// the same response as the download URL -- no second HTTP request
// needed, unlike Maven's provider (which needs a separate .sha512
// fetch). This is the key structural difference between the two.
func TestResolveAsset_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sampleVersions))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	asset, err := p.ResolveAsset(context.Background(), "8.14.5", "linux", "amd64")
	if err != nil {
		t.Fatalf("ResolveAsset failed: %v", err)
	}

	if asset.Filename != "gradle-8.14.5-bin.zip" {
		t.Errorf("expected filename derived from downloadUrl, got: %q", asset.Filename)
	}
	if asset.Checksum != "6f74b601422d6d6fc4e1f9a1ab6522f642c2fdcbc15ae33ebd30ba3d7198e854" {
		t.Errorf("expected checksum read directly from the response, got: %q", asset.Checksum)
	}
	if asset.ChecksumAlgorithm != registry.SHA256 {
		t.Errorf("expected SHA256, got: %s", asset.ChecksumAlgorithm)
	}
	if asset.URL != "https://services.gradle.org/distributions/gradle-8.14.5-bin.zip" {
		t.Errorf("unexpected URL: %s", asset.URL)
	}
}

func TestResolveAsset_RejectsNonFinalVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sampleVersions))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	// "9.7.0-rc-3" exists in the sample data but final=false -- since
	// fetchAllVersions already filters to final=true, ResolveAsset
	// correctly can't find it either.
	_, err := p.ResolveAsset(context.Background(), "9.7.0-rc-3", "linux", "amd64")
	if err != registry.ErrVersionNotFound {
		t.Errorf("expected ErrVersionNotFound for a non-final version, got: %v", err)
	}
}

func TestResolveAsset_IgnoresOSAndArch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sampleVersions))
	}))
	defer server.Close()

	p := &Provider{BaseURL: server.URL}
	linuxAsset, err := p.ResolveAsset(context.Background(), "9.7.1", "linux", "amd64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	windowsAsset, err := p.ResolveAsset(context.Background(), "9.7.1", "windows", "arm64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if linuxAsset.URL != windowsAsset.URL {
		t.Errorf("expected identical asset regardless of OS/arch, got %+v vs %+v", linuxAsset, windowsAsset)
	}
}

func TestCompareVersions_HandlesDoubleDigitsCorrectly(t *testing.T) {
	if compareVersions("8.10", "8.9") <= 0 {
		t.Error("expected 8.10 > 8.9 numerically, string comparison would get this backwards")
	}
}

func TestMajorOf(t *testing.T) {
	cases := map[string]string{
		"8.14.5": "8",
		"9.7.1":  "9",
		"7.6.6":  "7",
	}
	for in, want := range cases {
		got := majorOf(in)
		if got != want {
			t.Errorf("majorOf(%q) = %q, want %q", in, got, want)
		}
	}
}

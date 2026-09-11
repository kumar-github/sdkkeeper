// Package gradle implements registry.Provider for Gradle's own,
// single official distribution.
//
// Confirmed directly by fetching Gradle's own real, live API during
// design research -- genuinely the best-structured of the three
// providers built so far in this project:
//
//   - A real, dedicated JSON API exists (services.gradle.org/versions/),
//     unlike Maven (no API at all, just a static file archive) and
//     more directly comparable to Temurin/Liberica's own APIs.
//
//   - The checksum is embedded DIRECTLY in the same response as the
//     download URL -- no separate request needed at all (unlike
//     Maven, which needs a second fetch for its .sha512 file). SHA-256,
//     confirmed by the 64-hex-character checksum values and the
//     ".sha256" checksumUrl suffix.
//
//   - Gradle's own API provides an explicit, authoritative "final"
//     boolean directly -- true only for genuine, stable releases,
//     false for every RC/milestone/snapshot/nightly build. This is a
//     real, reliable filter straight from the source, not a string-
//     pattern heuristic (contrast with Maven, which has no such field
//     and needed a keyword-based guess instead).
//
//   - Like Maven, Gradle publishes exactly ONE universal,
//     cross-platform archive per version (pure Java plus a thin
//     launcher script) -- osName/arch are accepted here only to
//     satisfy the shared interface; they are genuinely ignored.
//
//   - No LTS concept exists for Gradle either -- one continuous
//     release line, not separate support tracks.
package gradle

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"sdkkeeper/internal/registry"
)

// DefaultBaseURL is Gradle's own real distribution/version service.
// Overridable for testing against a local httptest.Server, matching
// every other provider in this codebase.
const DefaultBaseURL = "https://services.gradle.org"

// Provider implements registry.Provider for Gradle.
type Provider struct {
	BaseURL    string
	HTTPClient *http.Client
}

func (p *Provider) Name() string { return "gradle" }

func (p *Provider) baseURL() string {
	if p.BaseURL != "" {
		return p.BaseURL
	}
	return DefaultBaseURL
}

func (p *Provider) client() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return http.DefaultClient
}

// release mirrors the real, confirmed JSON shape of one entry in
// services.gradle.org/versions/all -- field names taken directly from
// a real, live fetch of that exact endpoint during design research,
// restricted to the fields actually needed here.
type release struct {
	Version     string `json:"version"`
	DownloadURL string `json:"downloadUrl"`
	Checksum    string `json:"checksum"`
	Final       bool   `json:"final"`
}

// fetchAllVersions fetches and parses Gradle's own real version-list
// endpoint -- the single shared fetch behind ListMajorVersions,
// ListMajorVersionsWithLTS, ListPatchVersions, and ResolveAsset,
// matching the same shared-fetch pattern already established in every
// other provider in this codebase. Filters to Final == true directly
// -- Gradle's own API already distinguishes genuine stable releases
// from RC/milestone/snapshot/nightly builds, so no string-pattern
// guessing is needed here the way Maven's own provider needed.
func (p *Provider) fetchAllVersions(ctx context.Context) ([]release, error) {
	url := p.baseURL() + "/versions/all"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("gradle: building request: %w", err)
	}

	resp, err := p.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("gradle: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("gradle: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var parsed []release
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("gradle: could not parse response: %w", err)
	}

	var stable []release
	for _, r := range parsed {
		if r.Final {
			stable = append(stable, r)
		}
	}
	return stable, nil
}

// majorOf extracts the leading major version component, e.g.
// "8.14.5" -> "8".
func majorOf(version string) string {
	if i := strings.IndexByte(version, '.'); i != -1 {
		return version[:i]
	}
	return version
}

// ListMajorVersions implements registry.Provider.
func (p *Provider) ListMajorVersions(ctx context.Context) ([]string, error) {
	infos, err := p.ListMajorVersionsWithLTS(ctx)
	if err != nil {
		return nil, err
	}
	versions := make([]string, len(infos))
	for i, info := range infos {
		versions[i] = info.Number
	}
	return versions, nil
}

// ListMajorVersionsWithLTS implements registry.Provider. Gradle has no
// LTS release concept at all (one continuous release line, not
// separate support tracks), so LTS is always false here; the field
// still exists to satisfy the shared interface.
func (p *Provider) ListMajorVersionsWithLTS(ctx context.Context) ([]registry.MajorVersionInfo, error) {
	releases, err := p.fetchAllVersions(ctx)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var majors []int
	for _, r := range releases {
		m := majorOf(r.Version)
		n, err := strconv.Atoi(m)
		if err != nil || seen[m] {
			continue
		}
		seen[m] = true
		majors = append(majors, n)
	}
	if len(majors) == 0 {
		return nil, registry.ErrVersionNotFound
	}

	sort.Sort(sort.Reverse(sort.IntSlice(majors)))

	infos := make([]registry.MajorVersionInfo, len(majors))
	for i, m := range majors {
		infos[i] = registry.MajorVersionInfo{Number: strconv.Itoa(m), LTS: false}
	}
	return infos, nil
}

// ListPatchVersions implements registry.Provider. osName/arch are
// accepted only to satisfy the shared interface -- see the package
// doc for why they're genuinely irrelevant to Gradle's single,
// universal archive.
func (p *Provider) ListPatchVersions(ctx context.Context, major, osName, arch string) ([]string, error) {
	releases, err := p.fetchAllVersions(ctx)
	if err != nil {
		return nil, err
	}

	var patches []string
	for _, r := range releases {
		if majorOf(r.Version) == major {
			patches = append(patches, r.Version)
		}
	}
	if len(patches) == 0 {
		return nil, registry.ErrVersionNotFound
	}
	// See registry.DedupeStrings' own doc comment for why this is
	// applied uniformly across every provider's ListPatchVersions,
	// not just the one it was first reported against.
	patches = registry.DedupeStrings(patches)

	// Newest-first, matching the convention every other version list
	// in this tool already uses. Compares numeric dot-components
	// rather than raw strings -- plain string comparison would sort
	// "8.10" before "8.9" incorrectly (lexicographic, not numeric).
	sort.Slice(patches, func(i, j int) bool {
		return compareVersions(patches[i], patches[j]) > 0
	})
	return patches, nil
}

// compareVersions compares two dot-separated numeric version strings
// component by component (e.g. "8.10" > "8.9", which plain string
// comparison would get backwards). Returns >0 if a > b, <0 if a < b,
// 0 if equal. Non-numeric components compare as 0.
func compareVersions(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	for i := 0; i < len(aParts) || i < len(bParts); i++ {
		var an, bn int
		if i < len(aParts) {
			an, _ = strconv.Atoi(aParts[i])
		}
		if i < len(bParts) {
			bn, _ = strconv.Atoi(bParts[i])
		}
		if an != bn {
			return an - bn
		}
	}
	return 0
}

// ResolveAsset implements registry.Provider. osName/arch are accepted
// only to satisfy the shared interface -- see the package doc for why
// they're genuinely irrelevant to Gradle's single, universal archive.
func (p *Provider) ResolveAsset(ctx context.Context, version, osName, arch string) (registry.Asset, error) {
	releases, err := p.fetchAllVersions(ctx)
	if err != nil {
		return registry.Asset{}, err
	}

	for _, r := range releases {
		if r.Version != version {
			continue
		}
		if r.DownloadURL == "" || r.Checksum == "" {
			continue
		}
		// downloadUrl's own filename (e.g. "gradle-8.14.5-bin.zip") is
		// used directly, rather than reconstructed -- avoids
		// duplicating Gradle's own naming convention here.
		filename := r.DownloadURL
		if i := strings.LastIndexByte(filename, '/'); i != -1 {
			filename = filename[i+1:]
		}
		return registry.Asset{
			URL:               r.DownloadURL,
			Filename:          filename,
			Checksum:          strings.ToLower(r.Checksum),
			ChecksumAlgorithm: registry.SHA256,
		}, nil
	}

	return registry.Asset{}, registry.ErrVersionNotFound
}

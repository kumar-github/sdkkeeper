// Package gradle implements registry.Provider for Gradle's single
// official distribution.
//
// A real, dedicated JSON API exists (services.gradle.org/versions/),
// unlike Maven's static file archive. The checksum (SHA-256) is
// embedded directly in the same response as the download URL, no
// second request needed. The API provides an authoritative "final"
// boolean, true only for stable releases -- no string-pattern
// guessing needed, unlike Maven. Like Maven, Gradle publishes one
// universal, cross-platform archive per version, so osName/arch are
// accepted only to satisfy the shared interface. No LTS concept.
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

// DefaultBaseURL is Gradle's distribution/version service.
// Overridable for testing against a local httptest.Server.
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

// release mirrors the JSON shape of one entry in
// services.gradle.org/versions/all, restricted to the fields needed
// here.
type release struct {
	Version     string `json:"version"`
	DownloadURL string `json:"downloadUrl"`
	Checksum    string `json:"checksum"`
	Final       bool   `json:"final"`
}

// fetchAllVersions fetches and parses Gradle's version-list endpoint
// -- the single shared fetch behind ListMajorVersions,
// ListMajorVersionsWithLTS, ListPatchVersions, and ResolveAsset.
// Filters to Final == true directly, since Gradle's API already
// distinguishes stable releases from RC/milestone/nightly builds.
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

// ListMajorVersionsWithLTS implements registry.Provider. Gradle has
// no LTS concept, so LTS is always false; the field exists only to
// satisfy the shared interface.
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
// accepted only to satisfy the shared interface -- see the package doc.
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
	patches = registry.DedupeStrings(patches)

	// Newest-first; compares numeric dot-components since plain
	// string comparison sorts "8.10" before "8.9" incorrectly.
	sort.Slice(patches, func(i, j int) bool {
		return compareVersions(patches[i], patches[j]) > 0
	})
	return patches, nil
}

// compareVersions compares two dot-separated numeric version strings
// component by component. Returns >0/<0/0 for a>b/a<b/a==b.
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
// only to satisfy the shared interface -- see the package doc.
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
		// The URL's own filename is used directly, rather than
		// reconstructed, to avoid duplicating Gradle's naming convention.
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

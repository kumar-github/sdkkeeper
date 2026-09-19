// Package apache implements registry.Provider for Apache Maven's
// single official distribution.
//
// Differences from the JDK vendor providers (Temurin, Liberica):
//
//   - Maven has no candidates-style JSON API. Version discovery uses
//     Maven's standard repository metadata mechanism instead: since
//     Maven is published as a normal Maven Central artifact
//     (org.apache.maven:apache-maven), fetching its maven-metadata.xml
//     and parsing <versioning><versions><version> just works.
//   - Maven publishes one universal, cross-platform archive per
//     version, so osName/arch are accepted only to satisfy the shared
//     interface and are genuinely ignored.
//   - The stable download URL is Apache's archive mirror
//     (archive.apache.org), not repo1.maven.org or dlcdn.apache.org --
//     dlcdn only hosts the latest patch of each minor line and 404s
//     on older ones.
//   - The checksum algorithm is SHA-512, published as a hex digest
//     followed by a space and the filename (standard sha512sum
//     output shape), parsed by splitting on whitespace.
package apache

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"sdkkeeper/internal/registry"
)

// DefaultMetadataBaseURL is Maven Central's repository, used purely
// for version discovery. Overridable for testing against a local
// httptest.Server, matching every other provider.
const DefaultMetadataBaseURL = "https://repo1.maven.org/maven2"

// DefaultArchiveBaseURL is Apache's stable archive mirror, used for
// the actual download -- see the package doc for why not
// repo1.maven.org or dlcdn.apache.org.
const DefaultArchiveBaseURL = "https://archive.apache.org/dist/maven"

// Provider implements registry.Provider for Apache Maven.
type Provider struct {
	MetadataBaseURL string
	ArchiveBaseURL  string
	HTTPClient      *http.Client
}

func (p *Provider) Name() string { return "apache" }

func (p *Provider) metadataBaseURL() string {
	if p.MetadataBaseURL != "" {
		return p.MetadataBaseURL
	}
	return DefaultMetadataBaseURL
}

func (p *Provider) archiveBaseURL() string {
	if p.ArchiveBaseURL != "" {
		return p.ArchiveBaseURL
	}
	return DefaultArchiveBaseURL
}

func (p *Provider) client() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return http.DefaultClient
}

// mavenMetadata mirrors Maven's own standard, documented
// artifact-level repository metadata schema.
type mavenMetadata struct {
	Versioning struct {
		Versions struct {
			Version []string `xml:"version"`
		} `xml:"versions"`
	} `xml:"versioning"`
}

// isStableRelease excludes pre-release/development versions (alpha,
// beta, release candidates, snapshots).
func isStableRelease(version string) bool {
	lower := strings.ToLower(version)
	for _, marker := range []string{"alpha", "beta", "-rc", "snapshot"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}

// fetchAllVersions fetches and parses maven-metadata.xml for the
// apache-maven artifact -- the single shared fetch behind
// ListMajorVersions, ListMajorVersionsWithLTS, and ListPatchVersions.
func (p *Provider) fetchAllVersions(ctx context.Context) ([]string, error) {
	url := p.metadataBaseURL() + "/org/apache/maven/apache-maven/maven-metadata.xml"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("apache: building request: %w", err)
	}

	resp, err := p.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("apache: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("apache: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var parsed mavenMetadata
	if err := xml.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("apache: could not parse metadata: %w", err)
	}

	var stable []string
	for _, v := range parsed.Versioning.Versions.Version {
		if isStableRelease(v) {
			stable = append(stable, v)
		}
	}
	return stable, nil
}

// majorOf extracts the leading major version component, e.g.
// "3.9.9" -> "3".
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

// ListMajorVersionsWithLTS implements registry.Provider. Maven has no
// LTS concept (one continuous 3.x release history), so LTS is always
// false; the field exists only to satisfy the shared interface.
func (p *Provider) ListMajorVersionsWithLTS(ctx context.Context) ([]registry.MajorVersionInfo, error) {
	all, err := p.fetchAllVersions(ctx)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var majors []int
	for _, v := range all {
		m := majorOf(v)
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
	all, err := p.fetchAllVersions(ctx)
	if err != nil {
		return nil, err
	}

	var patches []string
	for _, v := range all {
		if majorOf(v) == major {
			patches = append(patches, v)
		}
	}
	if len(patches) == 0 {
		return nil, registry.ErrVersionNotFound
	}
	patches = registry.DedupeStrings(patches)

	// Newest-first; compares numeric dot-components since plain
	// string comparison sorts "3.10.0" before "3.9.9" incorrectly.
	sort.Slice(patches, func(i, j int) bool {
		return compareVersions(patches[i], patches[j]) > 0
	})
	return patches, nil
}

// compareVersions compares two dot-separated numeric version strings
// component by component. Returns >0/<0/0 for a>b/a<b/a==b.
// Non-numeric components compare as 0.
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
	filename := fmt.Sprintf("apache-maven-%s-bin.zip", version)
	archiveURL := fmt.Sprintf("%s/maven-%s/%s/binaries/%s", p.archiveBaseURL(), majorOf(version), version, filename)
	checksumURL := archiveURL + ".sha512"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checksumURL, nil)
	if err != nil {
		return registry.Asset{}, fmt.Errorf("apache: building checksum request: %w", err)
	}

	resp, err := p.client().Do(req)
	if err != nil {
		return registry.Asset{}, fmt.Errorf("apache: checksum request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// No checksum published (pre-3.3 releases) -- treated as not
		// offered rather than downloading unverifiable data.
		return registry.Asset{}, registry.ErrVersionNotFound
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return registry.Asset{}, fmt.Errorf("apache: unexpected checksum status %d: %s", resp.StatusCode, string(body))
	}

	rawChecksum, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return registry.Asset{}, fmt.Errorf("apache: reading checksum: %w", err)
	}

	// Either a bare hex digest, or a digest followed by a space and
	// the filename (sha512sum output shape) -- splitting on
	// whitespace and taking the first field handles both.
	fields := strings.Fields(string(rawChecksum))
	if len(fields) == 0 {
		return registry.Asset{}, fmt.Errorf("apache: empty checksum file for %s", filename)
	}
	checksum := strings.ToLower(fields[0])

	return registry.Asset{
		URL:               archiveURL,
		Filename:          filename,
		Checksum:          checksum,
		ChecksumAlgorithm: registry.SHA512,
	}, nil
}

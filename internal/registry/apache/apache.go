// Package apache implements registry.Provider for Apache Maven's own,
// single official distribution.
//
// Real, confirmed differences from every JDK vendor provider (Temurin,
// Liberica) in this codebase:
//
//   - Maven has no candidates-style JSON API at all. There is no
//     equivalent of Adoptium's /v3/info/available_releases or
//     BellSoft's /v1/liberica/releases. Version discovery instead uses
//     Maven's own STANDARD repository metadata mechanism -- confirmed
//     directly from Maven's own official documentation: an
//     artifact's maven-metadata.xml "serves purpose of version
//     discovery... contains list of versions of given GA
//     coordinates." Since Maven itself is published as a normal
//     Maven Central artifact (org.apache.maven:apache-maven), this
//     Just Works: fetch
//     https://repo1.maven.org/maven2/org/apache/maven/apache-maven/maven-metadata.xml
//     and parse its <versioning><versions><version> entries.
//
//   - Maven publishes exactly ONE universal, cross-platform archive
//     per version -- unlike a JDK (compiled, native code, genuinely
//     different per OS/arch), Maven is pure Java plus a thin
//     shell/bat launcher script, so osName/arch are accepted here
//     only to satisfy the shared registry.Provider interface; they
//     are genuinely ignored.
//
//   - The stable, "forever" download URL is Apache's own archive
//     mirror (archive.apache.org), NOT the metadata source
//     (repo1.maven.org) and NOT dlcdn.apache.org -- confirmed via a
//     real, live discussion (a GitHub Actions runner-images issue)
//     explaining exactly why: dlcdn.apache.org only hosts the LATEST
//     patch of each minor line and 404s on older ones, while
//     archive.apache.org serves every historical version
//     indefinitely.
//
//   - The checksum algorithm is SHA-512, confirmed directly from a
//     real Apache JIRA bug report (MNGSITE-378) showing the actual
//     raw content of a real .sha512 file: a hex digest followed by a
//     space and the filename (the same shape as standard `sha512sum`
//     output), not a bare hex string alone -- parsed here by
//     splitting on whitespace and taking the first field, which
//     handles either shape safely regardless of which is present for
//     a given historical release.
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

// DefaultMetadataBaseURL is Maven Central's own repository, used
// purely for version discovery (maven-metadata.xml). Overridable for
// testing against a local httptest.Server, matching every other
// provider in this codebase.
const DefaultMetadataBaseURL = "https://repo1.maven.org/maven2"

// DefaultArchiveBaseURL is Apache's own stable archive mirror, used
// for the actual download -- deliberately NOT repo1.maven.org (which
// hosts Maven Central's copy of the artifact's own metadata/POM, not
// necessarily a convenient binary distribution zip) and NOT
// dlcdn.apache.org (only hosts the latest patch per minor line, see
// package doc).
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
// beta, release candidates, snapshots) -- users typing a bare version
// number expect a real, finished release, matching how every other
// version list in this tool already behaves.
func isStableRelease(version string) bool {
	lower := strings.ToLower(version)
	for _, marker := range []string{"alpha", "beta", "-rc", "snapshot"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}

// fetchAllVersions fetches and parses Maven Central's own
// maven-metadata.xml for the apache-maven artifact -- the single
// shared fetch behind ListMajorVersions, ListMajorVersionsWithLTS, and
// ListPatchVersions, matching the same shared-fetch pattern already
// established in the temurin and liberica packages.
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
// LTS release concept at all (unlike a JDK, there is no separate
// "long-term support" line -- it's one continuous 3.x release
// history), so LTS is always false here; the field still exists to
// satisfy the shared interface.
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
// accepted only to satisfy the shared interface -- see the package
// doc for why they're genuinely irrelevant to Maven's single,
// universal archive.
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
	// See registry.DedupeStrings' own doc comment for why this is
	// applied uniformly across every provider's ListPatchVersions,
	// not just the one it was first reported against.
	patches = registry.DedupeStrings(patches)

	// Newest-first, matching the convention every other version list
	// in this tool already uses. Maven versions sort correctly as
	// plain strings far less reliably than integers do (e.g.
	// "3.10.0" vs "3.9.9" would sort wrong lexicographically), so
	// this compares numeric dot-components rather than raw strings.
	sort.Slice(patches, func(i, j int) bool {
		return compareVersions(patches[i], patches[j]) > 0
	})
	return patches, nil
}

// compareVersions compares two dot-separated numeric version strings
// component by component (e.g. "3.10.0" > "3.9.9", which plain string
// comparison would get backwards). Returns >0 if a > b, <0 if a < b,
// 0 if equal. Non-numeric components compare as 0 -- good enough for
// Maven's own version scheme, already filtered to stable releases by
// isStableRelease before this is ever called.
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
// they're genuinely irrelevant to Maven's single, universal archive.
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
		// No checksum published for this version -- treated as "this
		// version isn't offered through this provider" rather than
		// downloading unverifiable data. In practice this only
		// affects Maven releases old enough to predate SHA-512
		// publication (roughly pre-3.3), which nobody realistically
		// needs today.
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

	// The file's content is either a bare hex digest, or (confirmed
	// directly from a real Apache JIRA bug report showing actual file
	// content) a hex digest followed by a space and the filename,
	// matching standard `sha512sum` output shape. Splitting on
	// whitespace and taking the first field handles either shape
	// correctly.
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

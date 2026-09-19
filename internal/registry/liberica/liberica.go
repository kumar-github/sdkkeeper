// Package liberica implements registry.Provider against BellSoft's
// Product Discovery API (api.bell-sw.com), which serves Liberica JDK
// builds -- the second vendor added alongside Temurin.
//
// Differences from Temurin/Adoptium:
//   - OS naming differs: "macos", not "mac".
//   - Architecture is a two-axis system: Temurin uses one string
//     ("x64", "aarch64"); Liberica uses an arch family ("x86", "arm")
//     plus a separate bitness field (32/64).
//   - The response is a flat JSON array, not nested release+binaries
//     objects like Adoptium's.
//   - The checksum is SHA-1, not SHA-256.
//
// Unlike Temurin, BellSoft has no dedicated "list all major versions"
// endpoint -- ListMajorVersions is a best-effort derivation from a
// broad releases query; see ListMajorVersionsWithLTS.
package liberica

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"sdkkeeper/internal/registry"
)

// DefaultBaseURL is the real BellSoft API. Overridable (see
// Provider.BaseURL) for the same reason as Temurin's: testing against
// a local httptest.Server instead of live network.
const DefaultBaseURL = "https://api.bell-sw.com"

// Provider implements registry.Provider for Liberica.
type Provider struct {
	BaseURL    string
	HTTPClient *http.Client
}

func (p *Provider) Name() string { return "liberica" }

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

// libericaOS translates Go's runtime.GOOS to Liberica's own vocabulary
// -- confirmed directly from BellSoft's /v1/liberica/operating-systems
// discovery endpoint: ["linux", "linux-musl", "macos", "solaris",
// "windows"].
func libericaOS(goos string) (string, error) {
	switch goos {
	case "darwin":
		return "macos", nil
	case "linux":
		return "linux", nil
	case "windows":
		return "windows", nil
	default:
		return "", fmt.Errorf("liberica: unsupported OS %q", goos)
	}
}

// libericaArch translates Go's runtime.GOARCH to Liberica's two-axis
// arch+bitness system -- confirmed directly from BellSoft's
// /v1/liberica/architectures endpoint: ["arm", "ppc", "sparc",
// "riscv", "x86"] (a FAMILY, not a specific width -- bitness is
// reported/queried as its own separate field).
func libericaArch(goarch string) (arch string, bitness int, err error) {
	switch goarch {
	case "amd64":
		return "x86", 64, nil
	case "arm64":
		return "arm", 64, nil
	default:
		return "", 0, fmt.Errorf("liberica: unsupported architecture %q", goarch)
	}
}

// release mirrors the JSON shape of one entry in the flat array
// returned by /v1/liberica/releases, restricted to the fields actually
// needed here. Field names confirmed directly from BellSoft's own
// published API documentation response examples.
type release struct {
	Version        string `json:"version"`
	FeatureVersion int    `json:"featureVersion"`
	DownloadURL    string `json:"downloadUrl"`
	Filename       string `json:"filename"`
	SHA1           string `json:"sha1"`
	LTS            bool   `json:"LTS"`
}

// fetchReleases performs the actual HTTP call + JSON parsing shared by
// ResolveAsset and ListPatchVersions -- both need the exact same data
// (every release of one major/feature version, for one OS/arch/
// bitness), just used differently, matching the same shared-fetch
// pattern already established in the temurin package.
func (p *Provider) fetchReleases(ctx context.Context, major, goos, goarch string) ([]release, error) {
	os_, err := libericaOS(goos)
	if err != nil {
		return nil, err
	}
	arch, bitness, err := libericaArch(goarch)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("version-feature", major)
	q.Set("os", os_)
	q.Set("arch", arch)
	q.Set("bitness", strconv.Itoa(bitness))
	q.Set("package-type", "tar.gz")
	q.Set("bundle-type", "jdk")

	reqURL := p.baseURL() + "/v1/liberica/releases?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("liberica: building request: %w", err)
	}

	resp, err := p.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("liberica: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, registry.ErrVersionNotFound
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("liberica: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var parsed []release
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("liberica: could not parse response: %w", err)
	}
	return parsed, nil
}

// ResolveAsset implements registry.Provider.
func (p *Provider) ResolveAsset(ctx context.Context, version, goos, goarch string) (registry.Asset, error) {
	major, err := majorVersion(version)
	if err != nil {
		return registry.Asset{}, err
	}

	releases, err := p.fetchReleases(ctx, major, goos, goarch)
	if err != nil {
		return registry.Asset{}, err
	}

	for _, r := range releases {
		if stripBuildMetadata(r.Version) != version {
			continue
		}
		if r.DownloadURL == "" || r.SHA1 == "" {
			continue
		}
		return registry.Asset{
			URL:               r.DownloadURL,
			Filename:          r.Filename,
			Checksum:          strings.ToLower(r.SHA1),
			ChecksumAlgorithm: registry.SHA1,
		}, nil
	}

	return registry.Asset{}, registry.ErrVersionNotFound
}

// ListPatchVersions implements registry.Provider.
func (p *Provider) ListPatchVersions(ctx context.Context, major, goos, goarch string) ([]string, error) {
	releases, err := p.fetchReleases(ctx, major, goos, goarch)
	if err != nil {
		return nil, err
	}

	var versions []string
	for _, r := range releases {
		v := stripBuildMetadata(r.Version)
		if v == "" {
			continue
		}
		versions = append(versions, v)
	}
	if len(versions) == 0 {
		return nil, registry.ErrVersionNotFound
	}
	versions = registry.DedupeStrings(versions)

	// Explicitly sorted newest-first -- a real, live-reported bug:
	// this used to return versions in whatever order BellSoft's own
	// API happened to send them, unsorted, which turned out to be
	// neither alphabetical nor version-ordered in practice (e.g.
	// "25.0.1", "25.0.1", "25", "25.0.4", "25.0.4.1" in one real,
	// reported response). Unlike Temurin (see its own ListPatchVersions
	// comment on this exact same class of risk), no live confirmation
	// exists that BellSoft's API returns any particular order at all --
	// so this doesn't rely on one.
	sort.Slice(versions, func(i, j int) bool {
		return compareVersions(versions[i], versions[j]) > 0
	})
	return versions, nil
}

// compareVersions compares two dot-separated version strings
// numerically, component by component -- a missing trailing component
// compares as 0. Plain string comparison would sort "25.0.10" before
// "25.0.9" incorrectly.
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

// ListMajorVersions implements registry.Provider.
//
// Best-effort: no dedicated "list majors" endpoint exists, so this
// queries broadly (a fixed platform just to get a representative
// response -- the set of majors offered isn't expected to vary by
// platform) and derives the unique set of featureVersion values.
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

// ListMajorVersionsWithLTS implements registry.Provider. LTS is
// already present in the same response ListMajorVersions uses (an
// "LTS" field on each release).
//
// Deliberately does NOT send version-modifier=latest: that parameter
// is meant to pair with a specific version-feature=<major> filter
// ("the latest patch within this major"), not used bare to mean "one
// latest release per major" -- sent without a major filter, BellSoft
// returns just the single, globally newest release across all
// majors, which previously caused the picker to show only one major
// version. Omitting it returns the full release list instead, which
// the dedup loop below already handles correctly.
func (p *Provider) ListMajorVersionsWithLTS(ctx context.Context) ([]registry.MajorVersionInfo, error) {
	q := url.Values{}
	q.Set("os", "macos")
	q.Set("arch", "x86")
	q.Set("bitness", "64")
	q.Set("package-type", "tar.gz")
	q.Set("bundle-type", "jdk")

	reqURL := p.baseURL() + "/v1/liberica/releases?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("liberica: building request: %w", err)
	}

	resp, err := p.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("liberica: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("liberica: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var parsed []release
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("liberica: could not parse response: %w", err)
	}

	seen := make(map[int]bool)
	ltsByMajor := make(map[int]bool)
	var majors []int
	for _, r := range parsed {
		if r.FeatureVersion == 0 || seen[r.FeatureVersion] {
			continue
		}
		seen[r.FeatureVersion] = true
		ltsByMajor[r.FeatureVersion] = r.LTS
		majors = append(majors, r.FeatureVersion)
	}
	if len(majors) == 0 {
		return nil, registry.ErrVersionNotFound
	}

	sortDescending(majors) // newest-first

	infos := make([]registry.MajorVersionInfo, len(majors))
	for i, m := range majors {
		infos[i] = registry.MajorVersionInfo{
			Number: strconv.Itoa(m),
			LTS:    ltsByMajor[m],
		}
	}
	return infos, nil
}

func sortDescending(nums []int) {
	for i := 1; i < len(nums); i++ {
		for j := i; j > 0 && nums[j] > nums[j-1]; j-- {
			nums[j], nums[j-1] = nums[j-1], nums[j]
		}
	}
}

// stripBuildMetadata removes a vendor build-number suffix like "+11"
// from a version string, e.g. "11.0.5+11" -> "11.0.5" -- users type
// bare versions, never vendor build metadata.
func stripBuildMetadata(version string) string {
	if i := strings.IndexByte(version, '+'); i != -1 {
		return version[:i]
	}
	return version
}

// majorVersion extracts the leading major version number from a full
// version string, e.g. "21.0.2" -> "21".
func majorVersion(version string) (string, error) {
	if version == "" {
		return "", fmt.Errorf("liberica: empty version")
	}
	i := strings.IndexByte(version, '.')
	if i == -1 {
		// Legacy Liberica Java 8 identifiers use the "8uXXX" scheme
		// (e.g. "8u504") -- BellSoft's API rejects this string as-is
		// ("Unexpected parameter value"); the real major version is
		// the digit run before the "u". Checked digits-only so an
		// unrelated string containing a literal "u" isn't mismatched.
		if j := strings.IndexByte(version, 'u'); j > 0 && isAllDigits(version[:j]) {
			return version[:j], nil
		}
		return version, nil
	}
	return version[:i], nil
}

// isAllDigits reports whether s is non-empty and consists entirely of
// ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

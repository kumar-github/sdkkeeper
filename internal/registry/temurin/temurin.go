// Package temurin implements registry.Provider against the Adoptium
// API (api.adoptium.net), which serves Eclipse Temurin builds -- the
// mainstream-default JDK vendor (design doc §7). Field names and
// OS/arch vocabulary below are taken directly from Adoptium's own
// published documentation and example scripts (api.adoptium.net docs +
// cookbook.adoc) and, where noted, confirmed against real, live
// responses retrieved directly by the user.
package temurin

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

// DefaultBaseURL is the real Adoptium API. Overridable (see
// Provider.BaseURL) specifically so this package can be tested against
// a local httptest.Server instead of the live network -- this sandbox's
// own network allowlist doesn't include api.adoptium.net, so this is
// how the fetch/parse/error-handling logic is verified here.
const DefaultBaseURL = "https://api.adoptium.net"

// Provider implements registry.Provider for Temurin.
type Provider struct {
	// BaseURL defaults to DefaultBaseURL when empty (zero value usable
	// directly as &Provider{} in production code).
	BaseURL string

	// HTTPClient defaults to http.DefaultClient when nil.
	HTTPClient *http.Client
}

func (p *Provider) Name() string { return "temurin" }

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

// adoptiumOS translates Go's runtime.GOOS to Adoptium's own vocabulary
// ("mac", not "darwin"/"macos") -- confirmed directly from Adoptium's
// published docs (cookbook.adoc: "one of linux, windows, mac, solaris,
// aix, or alpine-linux").
// adoptiumOS translates Go's runtime.GOOS to Adoptium's own vocabulary
// -- confirmed directly from Adoptium's own published API docs and
// CI/CD cookbook (both list "windows" as a real, valid {os} value,
// alongside "linux"/"mac"/"alpine-linux"/"aix"/"solaris").
func adoptiumOS(goos string) (string, error) {
	switch goos {
	case "darwin":
		return "mac", nil
	case "linux":
		return "linux", nil
	case "windows":
		return "windows", nil
	default:
		return "", fmt.Errorf("temurin: unsupported OS %q", goos)
	}
}

// adoptiumArch translates Go's runtime.GOARCH to Adoptium's own
// vocabulary ("aarch64", not "arm64") -- confirmed directly from
// Adoptium's published docs.
func adoptiumArch(goarch string) (string, error) {
	switch goarch {
	case "amd64":
		return "x64", nil
	case "arm64":
		return "aarch64", nil
	default:
		return "", fmt.Errorf("temurin: unsupported architecture %q", goarch)
	}
}

// featureReleasesResponse mirrors the JSON shape returned by
// /v3/assets/feature_releases/{major}/ga, restricted to the fields
// actually needed here. binary.package.name/.checksum/.link were
// correctly guessed from Adoptium's PowerShell cookbook example --
// but the top-level version object's real key is "version_data", NOT
// "version" as originally assumed and never verified. That wrong
// assumption was a real, live bug: since the JSON key never matched,
// OpenJDKVersion/Semver silently deserialized as empty strings for
// every release, meaning NO version could ever match, regardless of
// what was requested. Confirmed and fixed against a real, live
// response, retrieved by the user directly via curl+jq.
type featureReleasesResponse []struct {
	Version struct {
		OpenJDKVersion string `json:"openjdk_version"`
		Semver         string `json:"semver"`
	} `json:"version_data"`
	Binaries []struct {
		Package struct {
			Name     string `json:"name"`
			Link     string `json:"link"`
			Checksum string `json:"checksum"`
		} `json:"package"`
	} `json:"binaries"`
}

// fetchFeatureReleases performs the actual HTTP call + JSON parsing
// shared by ResolveAsset and ListPatchVersions -- both need the exact
// same data (every release of one major/feature version, for one
// OS/arch), just used differently (search for one exact match vs.
// list every version found). Extracted here so that shared logic --
// including the page_size fix below -- is written, and fixed, in
// exactly one place.
func (p *Provider) fetchFeatureReleases(ctx context.Context, major, goos, goarch string) (featureReleasesResponse, error) {
	os_, err := adoptiumOS(goos)
	if err != nil {
		return nil, err
	}
	arch, err := adoptiumArch(goarch)
	if err != nil {
		return nil, err
	}

	// page_size is deliberately large (not the API's small default,
	// confirmed live to be 10) -- a real, confirmed bug: without this,
	// an older patch (e.g. 21.0.2, superseded by 13+ newer releases of
	// the same major by now) silently fell outside the default page
	// and was reported as "not found" even though it's a completely
	// real, valid, installable release. 100 is a generous margin
	// above the 14 total releases confirmed for major 21 -- some
	// long-lived majors (e.g. 8, maintained since 2014) may have more
	// history than 21 does, so this isn't tuned to exactly what 21
	// happens to need today.
	url := fmt.Sprintf("%s/v3/assets/feature_releases/%s/ga?os=%s&architecture=%s&image_type=jdk&page_size=100",
		p.baseURL(), major, os_, arch)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("temurin: building request: %w", err)
	}

	resp, err := p.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("temurin: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, registry.ErrVersionNotFound
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("temurin: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var parsed featureReleasesResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("temurin: could not parse response: %w", err)
	}
	return parsed, nil
}

// ResolveAsset implements registry.Provider.
func (p *Provider) ResolveAsset(ctx context.Context, version, goos, goarch string) (registry.Asset, error) {
	major, err := majorVersion(version)
	if err != nil {
		return registry.Asset{}, err
	}

	parsed, err := p.fetchFeatureReleases(ctx, major, goos, goarch)
	if err != nil {
		return registry.Asset{}, err
	}

	for _, release := range parsed {
		relVersion := stripBuildMetadata(release.Version.OpenJDKVersion)
		relSemver := stripBuildMetadata(release.Version.Semver)
		if relVersion != version && relSemver != version {
			continue
		}
		if len(release.Binaries) == 0 {
			continue
		}
		pkg := release.Binaries[0].Package
		if pkg.Link == "" || pkg.Checksum == "" {
			continue
		}
		return registry.Asset{
			URL:               pkg.Link,
			Filename:          pkg.Name,
			Checksum:          strings.ToLower(pkg.Checksum),
			ChecksumAlgorithm: registry.SHA256,
		}, nil
	}

	return registry.Asset{}, registry.ErrVersionNotFound
}

// ListPatchVersions implements registry.Provider. Returns every patch
// version found for major, explicitly sorted newest-first.
//
// A real, confirmed risk this guards against, even though not yet
// symptomatic here the way it was for Liberica (see that provider's
// own ListPatchVersions comment): this used to rely on an UNVERIFIED
// assumption that Adoptium's own API always returns results already
// sorted newest-first, based on a one-time, point-in-time observation
// rather than a documented guarantee. Confirmed directly from
// Adoptium's own API cookbook: getting a genuinely sorted response
// requires EXPLICITLY passing sort_method=DATE&sort_order=DESC --
// this provider's own request never did, so any apparent ordering was
// incidental, not contractual. Sorting explicitly here removes that
// assumption entirely, regardless of whatever order the API actually
// happens to return.
func (p *Provider) ListPatchVersions(ctx context.Context, major, goos, goarch string) ([]string, error) {
	parsed, err := p.fetchFeatureReleases(ctx, major, goos, goarch)
	if err != nil {
		return nil, err
	}

	var versions []string
	for _, release := range parsed {
		v := stripBuildMetadata(release.Version.OpenJDKVersion)
		if v == "" {
			continue
		}
		versions = append(versions, v)
	}
	if len(versions) == 0 {
		return nil, registry.ErrVersionNotFound
	}
	versions = registry.DedupeStrings(versions)
	sort.Slice(versions, func(i, j int) bool {
		return compareVersions(versions[i], versions[j]) > 0
	})
	return versions, nil
}

// compareVersions compares two dot-separated version strings
// numerically, component by component -- a missing trailing component
// compares as 0, so versions with different numbers of components
// (e.g. "21" vs "21.0.2") still compare correctly. Mirrors the
// identical, already-established pattern in apache's and gradle's own
// compareVersions (and liberica's, added for the same reason).
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

// availableReleasesResponse mirrors /v3/info/available_releases,
// confirmed directly from Adoptium's own cookbook example.
type availableReleasesResponse struct {
	AvailableReleases []int `json:"available_releases"`
	// AvailableLTSReleases was already being fetched (same response,
	// no extra network call) but not read until ListMajorVersionsWithLTS
	// needed it -- confirmed present directly in Adoptium's own
	// documented example response.
	AvailableLTSReleases []int `json:"available_lts_releases"`
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

// ListMajorVersionsWithLTS implements registry.Provider.
func (p *Provider) ListMajorVersionsWithLTS(ctx context.Context) ([]registry.MajorVersionInfo, error) {
	url := p.baseURL() + "/v3/info/available_releases"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("temurin: building request: %w", err)
	}

	resp, err := p.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("temurin: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("temurin: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var parsed availableReleasesResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("temurin: could not parse response: %w", err)
	}

	ltsSet := make(map[int]bool, len(parsed.AvailableLTSReleases))
	for _, n := range parsed.AvailableLTSReleases {
		ltsSet[n] = true
	}

	// Newest-first, matching the convention every other version list
	// in this tool already uses (inventory.Scan sorts descending too).
	infos := make([]registry.MajorVersionInfo, len(parsed.AvailableReleases))
	for i, n := range parsed.AvailableReleases {
		infos[len(parsed.AvailableReleases)-1-i] = registry.MajorVersionInfo{
			Number: fmt.Sprintf("%d", n),
			LTS:    ltsSet[n],
		}
	}
	return infos, nil
}

// stripBuildMetadata removes a vendor build-number suffix like "+13"
// from a version string, e.g. "21.0.2+13" -> "21.0.2". Adoptium's own
// version strings always include this build metadata, but users
// naturally think and type in terms of the bare major.minor.patch
// triple, matching every other command in this tool (e.g. "sk use
// java 21.0.2", never "21.0.2+13") -- without stripping this, an exact
// string match would never succeed for a normally-typed version.
//
// Simplification accepted for now: if the SAME stripped version has
// multiple build revisions (e.g. both "21.0.2+13" and "21.0.2+14"
// exist), the first one encountered in the API's response order wins.
// Pinning to an exact build number is not supported yet -- a contained
// future addition, not needed for the common case.
//
// Confirmed real quirk (via a live response, retrieved by the user):
// for a version with a 4-component patch number (e.g. openjdk_version
// "21.0.12.1+1-LTS"), Adoptium's separate `semver` field encodes that
// differently and, after stripping, is actually SHORTER --
// "21.0.12+101.0.LTS" strips to "21.0.12", silently dropping the ".1"
// that openjdk_version correctly preserves ("21.0.12.1"). Matching
// still ORs both fields (see ResolveAsset), so this doesn't cause
// openjdk_version's correct full match to fail -- but it does mean a
// user typing the shorter "21.0.12" (omitting the real ".1") could
// match via semver when the fully-correct version actually has that
// 4th component. Accepted as a known, narrow imprecision for now.
func stripBuildMetadata(version string) string {
	if i := strings.IndexByte(version, '+'); i != -1 {
		return version[:i]
	}
	return version
}

// majorVersion extracts the leading major version number from a full
// version string, e.g. "21.0.2" -> "21" -- Adoptium's
// feature_releases endpoint is scoped by major/feature version only,
// so this is needed regardless of which exact patch is requested.
func majorVersion(version string) (string, error) {
	if version == "" {
		return "", fmt.Errorf("temurin: empty version")
	}
	i := strings.IndexByte(version, '.')
	if i == -1 {
		return version, nil
	}
	first := version[:i]

	// Java 8 and earlier used the legacy "1.X" scheme, e.g.
	// "1.8.0_482-b08" -- the real major version is the SECOND
	// component ("8"), not the first ("1"). Confirmed as a real,
	// live bug: the patch picker correctly listed "1.8.0_482-b08"
	// (from Adoptium's own openjdk_version string), but resolving it
	// afterward queried major "1" instead of "8", which doesn't
	// exist, so install always failed with "not found" for any Java
	// 8 release.
	if first == "1" {
		rest := version[i+1:]
		if j := strings.IndexByte(rest, '.'); j != -1 {
			return rest[:j], nil
		}
		return rest, nil
	}
	return first, nil
}

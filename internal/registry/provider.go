// Package registry defines the abstraction between "sk knows how to
// install a version of a tool" and "here's how a specific vendor's API
// actually works." Every vendor-specific quirk (OS/arch naming
// conventions, API shape, how a checksum is obtained) lives entirely
// inside that vendor's own implementation -- the installer package
// that consumes this interface never contains a vendor-specific
// conditional (design doc §11.4).
package registry

import (
	"context"
	"errors"
)

// ErrVersionNotFound is returned by a Provider when the requested
// version genuinely doesn't exist in that vendor's catalog -- distinct
// from a network/transport error, so callers can give a clear "no such
// version" message instead of a confusing low-level error.
var ErrVersionNotFound = errors.New("version not found in vendor catalog")

// Checksum algorithm names, shared so every Provider implementation
// (and the installer package that verifies against them) refers to the
// same exact strings, not ad-hoc "sha256"/"SHA256"/"Sha-256" spelling
// scattered across files.
const (
	SHA256 = "sha256"
	SHA1   = "sha1"
	// SHA512 -- added for Apache Maven, which publishes SHA-512
	// checksums for its own release archives (confirmed directly:
	// archive.apache.org's own .sha512 files, standard sha512sum-style
	// content). A third real vendor, a third real algorithm -- exactly
	// why this was generalized instead of assumed fixed.
	SHA512 = "sha512"
)

// Asset is everything needed to download and verify one specific,
// resolved binary -- exactly one version, for exactly one OS/arch.
type Asset struct {
	// URL is the direct download link for the archive.
	URL string

	// Filename is the archive's own filename (e.g.
	// "OpenJDK21U-jdk_x64_mac_hotspot_21.0.2_13.tar.gz"), used for
	// user-facing progress messages, not for anything structural.
	Filename string

	// Checksum is the expected checksum, lowercase hex, no prefix.
	Checksum string

	// ChecksumAlgorithm names which hash Checksum is -- "sha256" or
	// "sha1". Not every vendor publishes the same algorithm: Temurin
	// (Adoptium) publishes SHA-256, but Liberica (BellSoft) only
	// publishes SHA-1 (confirmed directly from their own API's
	// documented response examples -- every one includes a "sha1"
	// field, none include "sha256"). Generalized here rather than
	// assuming one algorithm, specifically so the installer package
	// can verify correctly regardless of which vendor resolved the
	// asset.
	ChecksumAlgorithm string
}

// Provider is implemented once per vendor (Temurin, Liberica, ...).
// Adding a new vendor later means implementing this interface in a new
// package -- never touching the installer or cli packages that consume
// it.
type Provider interface {
	// Name is the vendor identifier used on the command line and in
	// version-folder suffixes, e.g. "temurin".
	Name() string

	// ResolveAsset looks up the download URL and checksum for an exact
	// version, for the given OS ("darwin"/"linux", i.e.
	// runtime.GOOS -- translated to this vendor's own naming
	// convention internally) and arch ("amd64"/"arm64", i.e.
	// runtime.GOARCH, same deal). Returns ErrVersionNotFound if the
	// version doesn't exist for this vendor/OS/arch combination.
	ResolveAsset(ctx context.Context, version, osName, arch string) (Asset, error)

	// ListPatchVersions returns every available patch version for one
	// major/feature version (e.g. major="21" -> "21", "21.0.1", ...,
	// "21.0.12.1"), for the given OS/arch. Used to show an interactive
	// picker when a user gives only a major version (e.g. `sk install
	// java 21`) instead of an exact patch.
	ListPatchVersions(ctx context.Context, major, osName, arch string) ([]string, error)

	// ListMajorVersions returns every major/feature version this
	// vendor currently has available (e.g. "21", "17", "11", "8").
	// Used to show an interactive picker when a user gives NO version
	// at all (`sk install java`), before the patch-version picker.
	ListMajorVersions(ctx context.Context) ([]string, error)

	// ListMajorVersionsWithLTS is like ListMajorVersions, but also
	// reports each major's LTS status -- used by `search`'s richer
	// display. Kept as a SEPARATE method rather than changing
	// ListMajorVersions's own signature, so `install`'s existing
	// picker flow (which only ever needed bare strings) doesn't need
	// to change at all.
	ListMajorVersionsWithLTS(ctx context.Context) ([]MajorVersionInfo, error)
}

// MajorVersionInfo describes one major/feature version a vendor
// offers, including whether it's a current LTS release -- both
// Temurin's and Liberica's own APIs already report this directly (no
// extra network call needed beyond what ListMajorVersions already
// makes; it was simply not being read before this was added).
type MajorVersionInfo struct {
	Number string
	LTS    bool
}

// DedupeStrings returns items with duplicates removed, preserving the
// order of each value's FIRST occurrence -- shared by every provider's
// own ListPatchVersions, unlike OS/arch translation, which genuinely
// differs per vendor and is deliberately NOT unified (see each
// provider's own os/arch functions): this specific operation (collect
// a list of already-vendor-specific strings, then de-duplicate it) has
// no vendor-specific shape at all, so duplicating it per-provider
// would be REAL, avoidable duplication, not the same kind of
// structural difference that keeps OS/arch translation separate.
//
// A real, live-reported bug this fixes: a vendor can legitimately
// publish more than one distinct release (e.g. a rebuild shortly
// after an initial release) that both strip down to the exact same
// bare patch version -- e.g. two releases both reported as "22.0.1"
// once build metadata is removed. Every provider's own
// ListPatchVersions built its version list with no such check at all,
// so the exact same bare version could appear twice in a row in `sk
// search`'s own output, for any provider, not just the one it was
// first reported against.
func DedupeStrings(items []string) []string {
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		if seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

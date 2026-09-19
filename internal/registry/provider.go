// Package registry defines the abstraction between "sk knows how to
// install a version of a tool" and "here's how a specific vendor's
// API actually works." Every vendor-specific quirk (OS/arch naming,
// API shape, checksum retrieval) lives entirely inside that vendor's
// own implementation -- installer never contains a vendor-specific
// conditional.
package registry

import (
	"context"
	"errors"
)

// ErrVersionNotFound is returned when the requested version genuinely
// doesn't exist in that vendor's catalog, distinct from a network
// error.
var ErrVersionNotFound = errors.New("version not found in vendor catalog")

// Checksum algorithm names, shared so every Provider implementation
// (and the installer package that verifies against them) refers to the
// same exact strings, not ad-hoc "sha256"/"SHA256"/"Sha-256" spelling
// scattered across files.
const (
	SHA256 = "sha256"
	SHA1   = "sha1"
	SHA512 = "sha512" // Apache Maven publishes SHA-512 checksums
)

// Asset is everything needed to download and verify one specific,
// resolved binary -- exactly one version, for exactly one OS/arch.
type Asset struct {
	// URL is the direct download link for the archive.
	URL string

	// Filename is the archive's own filename, used for user-facing
	// progress messages, not anything structural.
	Filename string

	// Checksum is the expected checksum, lowercase hex, no prefix.
	Checksum string

	// ChecksumAlgorithm names which hash Checksum is. Not every
	// vendor publishes the same one: Temurin publishes SHA-256,
	// Liberica only SHA-1.
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
	// version, OS (runtime.GOOS, translated to this vendor's own
	// naming), and arch (runtime.GOARCH). Returns ErrVersionNotFound
	// if the version doesn't exist for this combination.
	ResolveAsset(ctx context.Context, version, osName, arch string) (Asset, error)

	// ListPatchVersions returns every patch version for one major/
	// feature version, for the given OS/arch -- used for the picker
	// when a user gives only a major version.
	ListPatchVersions(ctx context.Context, major, osName, arch string) ([]string, error)

	// ListMajorVersions returns every major/feature version currently
	// available -- used for the picker when a user gives no version.
	ListMajorVersions(ctx context.Context) ([]string, error)

	// ListMajorVersionsWithLTS is like ListMajorVersions, but also
	// reports each major's LTS status, for `search`'s richer display.
	// A separate method so install's existing picker flow (bare
	// strings) doesn't need to change.
	ListMajorVersionsWithLTS(ctx context.Context) ([]MajorVersionInfo, error)
}

// MajorVersionInfo describes one major/feature version a vendor
// offers, including whether it's a current LTS release.
type MajorVersionInfo struct {
	Number string
	LTS    bool
}

// DedupeStrings returns items with duplicates removed, preserving
// first-occurrence order -- shared by every provider's own
// ListPatchVersions. A vendor can publish more than one release (e.g.
// a rebuild) that strips down to the same bare patch version, which
// would otherwise appear twice in `sk search`'s output.
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

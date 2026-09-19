package cli

import (
	"sort"

	"sdkkeeper/internal/registry"
	"sdkkeeper/internal/registry/apache"
	"sdkkeeper/internal/registry/gradle"
	"sdkkeeper/internal/registry/liberica"
	"sdkkeeper/internal/registry/temurin"
	"sdkkeeper/internal/tooldef"
)

// namedProvider pairs a Provider with its registered vendor name. A
// slice, not a map, preserves each tool's vendor order deliberately
// (Temurin before Liberica, this project's mainstream-default JDK
// vendor) rather than an incidental one -- a map's sort.Strings()
// would silently alphabetize this to "liberica, temurin".
type namedProvider struct {
	name     string
	provider registry.Provider
}

// providersByTool maps each tool name to its ordered set of vendor
// Providers. A tool with no entry (or an empty slice) has no install
// support yet -- checked generically by install/vendors, not a
// hardcoded per-tool check. A tool with exactly one provider (every
// non-JDK tool currently registered) never shows a vendor picker or
// gets a vendor suffix -- see hasSingleVendor's callers. Adding a new
// tool's install support means adding one entry here.
var providersByTool = map[string][]namedProvider{
	"java": {
		{"temurin", &temurin.Provider{}},
		{"liberica", &liberica.Provider{}},
	},
	"maven": {
		{"apache", &apache.Provider{}},
	},
	"gradle": {
		{"gradle", &gradle.Provider{}},
	},
}

// providersFor returns a tool's vendor Providers keyed by name (nil
// if unsupported). For deliberate ORDER, use vendorNamesFor instead;
// this form is for direct "do I have a provider named X" lookups.
func providersFor(toolName string) map[string]registry.Provider {
	ordered := providersByTool[toolName]
	if len(ordered) == 0 {
		return nil
	}
	m := make(map[string]registry.Provider, len(ordered))
	for _, np := range ordered {
		m[np.name] = np.provider
	}
	return m
}

// vendorNamesFor returns a tool's vendor names in the deliberate
// order registered in providersByTool -- not alphabetical.
func vendorNamesFor(toolName string) []string {
	ordered := providersByTool[toolName]
	names := make([]string, len(ordered))
	for i, np := range ordered {
		names[i] = np.name
	}
	return names
}

// hasSingleVendor reports whether a tool has EXACTLY one known
// provider -- the condition that makes a vendor picker and a vendor
// suffix on version identifiers both unnecessary.
func hasSingleVendor(toolName string) bool {
	return len(vendorNamesFor(toolName)) == 1
}

// installSupportedTools returns the display names of every tool with
// at least one registered provider, alphabetically (no ordering
// precedent applies across tools the way it does among JDK vendors).
func installSupportedTools() []string {
	names := make([]string, 0, len(providersByTool))
	for name := range providersByTool {
		names = append(names, name)
	}
	sort.Strings(names)

	display := make([]string, 0, len(names))
	for _, name := range names {
		if tool, ok := tooldef.Get(name); ok {
			display = append(display, tool.DisplayName)
		}
	}
	return display
}

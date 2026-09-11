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

// namedProvider pairs a Provider with the vendor name it's registered
// under. A slice of these, not a map, is what preserves each tool's
// vendor list in a DELIBERATE order rather than an incidental one --
// Temurin is listed before Liberica here because it's this project's
// own established "mainstream default" JDK vendor (a real distinction
// from early in this project, not a coincidence of alphabetical
// sorting), and that ordering matters for which vendor a user sees
// first in the picker and in `vendors`/`search` output. A caught
// regression during a refactor: an earlier version of this file used
// sort.Strings() on a map's keys, which silently reordered this to
// "liberica, temurin" (alphabetical) -- technically stable and
// deterministic, but wrong, since it lost a genuinely meaningful
// choice for an accidental one. Fixed by switching to this
// slice-based structure, which has no unordered map step to
// accidentally re-sort away.
type namedProvider struct {
	name     string
	provider registry.Provider
}

// providersByTool maps each tool name to its own ordered set of
// vendor Providers.
//
// A tool with NO entry here (or an empty slice) has no install
// support yet -- `install` and `vendors` both check this generically,
// rather than a hardcoded "is this java?" check, so a new tool
// automatically gets correct "not yet supported" behavior the moment
// before its own entry is added, and correct real behavior the moment
// after.
//
// A tool with EXACTLY ONE provider -- true of every non-JDK tool
// currently registered in tooldef (maven, gradle, node, kafka each
// have exactly one canonical upstream source, not competing vendors,
// confirmed directly by their own FolderPrefix values already baking
// in that one vendor, e.g. "apache-maven-") -- never shows a vendor
// picker and never gets a vendor suffix on its version identifiers:
// with only one possible source, there's no real choice to present
// and no ambiguity a suffix would need to resolve. This distinction
// is load-bearing throughout install.go, not just cosmetic -- see
// hasSingleVendor's callers.
//
// Adding a new tool's install support means adding one entry here --
// no changes needed to install.go, search.go, vendors.go, or doctor.go,
// which all read this generically.
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

// providersFor returns the known vendor Providers for a tool, keyed
// by vendor name -- empty (possibly nil) if that tool has no install
// support yet. Callers that need the deliberate ORDER (pickers,
// display output) should use vendorNamesFor instead and look up each
// name here; this form is for direct "do I have a provider named X"
// lookups, e.g. parsing a user-typed vendor suffix.
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
// order they're registered in providersByTool -- NOT alphabetical,
// and not a map's incidental iteration order either. See
// providersByTool's own comment for why this distinction is real, not
// cosmetic.
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

// installSupportedTools returns the display names of every tool that
// currently has at least one registered provider. Alphabetical here
// is fine (unlike vendor order within one tool, no tool has an
// established "should appear first" precedent the way Temurin does
// among JDK vendors) -- used to build an accurate "currently
// supported: X, Y" message that stays correct automatically as more
// tools gain install support, rather than a hardcoded string that
// would silently go stale the moment a second tool is added.
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

package cli

import (
	"context"
	"testing"

	"sdkkeeper/internal/registry"
)

func TestVendorNamesFor_StableOrder(t *testing.T) {
	// vendorNamesFor feeds the picker directly -- must be the same
	// order every call (Go map iteration is randomized), or the
	// picker's options would shuffle between runs.
	first := vendorNamesFor("java")
	for i := 0; i < 10; i++ {
		got := vendorNamesFor("java")
		if len(got) != len(first) {
			t.Fatalf("expected consistent length, got %d vs %d", len(got), len(first))
		}
		for j := range first {
			if got[j] != first[j] {
				t.Errorf("expected stable order across calls, got %v vs %v", got, first)
			}
		}
	}
}

func TestProvidersFor_MatchesVendorNames(t *testing.T) {
	providers := providersFor("java")
	for _, name := range vendorNamesFor("java") {
		p, ok := providers[name]
		if !ok {
			t.Errorf("vendorNamesFor(java) includes %q but providersFor(java) has no entry for it", name)
			continue
		}
		if p.Name() != name {
			t.Errorf("provider registered under key %q reports Name() = %q -- must match", name, p.Name())
		}
	}
}

// TestVendorNamesFor_UnknownTool confirms a tool with no registered
// providers at all (java, maven, and gradle all have providers now;
// node and kafka still don't) returns an empty slice rather than
// panicking or returning nil in a way that would confuse a
// len() == 0 check elsewhere.
func TestVendorNamesFor_UnknownTool(t *testing.T) {
	names := vendorNamesFor("node")
	if len(names) != 0 {
		t.Errorf("expected no vendors registered for node yet, got: %v", names)
	}
}

func TestHasSingleVendor_JavaIsMultiVendor(t *testing.T) {
	if hasSingleVendor("java") {
		t.Error("expected java (2 vendors: temurin, liberica) to NOT be single-vendor")
	}
}

// TestHasSingleVendor_UnregisteredToolIsNotSingleVendor confirms a
// tool with ZERO providers is correctly distinct from a tool with
// exactly one -- hasSingleVendor should only be true for the specific
// "exactly one" case, not "one or fewer".
func TestHasSingleVendor_UnregisteredToolIsNotSingleVendor(t *testing.T) {
	if hasSingleVendor("node") {
		t.Error("expected a tool with zero registered providers to NOT be reported as single-vendor")
	}
}

// TestHasSingleVendor_MavenIsSingleVendor confirms the real, live
// behavior the whole conditional design was built for -- Maven now
// has exactly one registered provider (apache), so it should skip the
// vendor picker and accept a bare version with no suffix, exactly
// like the forward-looking fake-tool test proved before Maven's own
// provider existed to test this for real.
func TestHasSingleVendor_MavenIsSingleVendor(t *testing.T) {
	if !hasSingleVendor("maven") {
		t.Error("expected maven (exactly one provider: apache) to be reported as single-vendor")
	}
}

func TestHasSingleVendor_GradleIsSingleVendor(t *testing.T) {
	if !hasSingleVendor("gradle") {
		t.Error("expected gradle (exactly one provider: gradle) to be reported as single-vendor")
	}
}

func TestInstallSupportedTools_IncludesJavaMavenAndGradle(t *testing.T) {
	supported := installSupportedTools()
	wantAll := map[string]bool{"JDK": false, "Maven": false, "Gradle": false}
	for _, name := range supported {
		if _, ok := wantAll[name]; ok {
			wantAll[name] = true
		}
	}
	for name, found := range wantAll {
		if !found {
			t.Errorf("expected %q in installSupportedTools(), got: %v", name, supported)
		}
	}
}

// TestParseFullIdentifier_SingleVendorAcceptsBareVersion is a
// forward-looking test using a temporary, single-provider tool
// registration -- confirms the actual behavior a tool like Maven will
// get once it has exactly one provider: a bare version with no vendor
// suffix is treated as a complete identifier, since there's no real
// ambiguity to resolve when only one source exists. An explicit
// vendor suffix must ALSO still work, for forward-compatibility if
// that tool ever gains a second vendor later.
func TestParseFullIdentifier_SingleVendorAcceptsBareVersion(t *testing.T) {
	original, hadOriginal := providersByTool["testtool"]
	providersByTool["testtool"] = []namedProvider{
		{"apache", &fakeProvider{name: "apache"}},
	}
	defer func() {
		if hadOriginal {
			providersByTool["testtool"] = original
		} else {
			delete(providersByTool, "testtool")
		}
	}()

	version, vendor, ok := parseFullIdentifier("testtool", "3.9.14")
	if !ok {
		t.Fatal("expected a bare version to be accepted as complete for a single-vendor tool")
	}
	if version != "3.9.14" || vendor != "apache" {
		t.Errorf("expected version=3.9.14 vendor=apache, got version=%q vendor=%q", version, vendor)
	}

	// Explicit suffix must ALSO still work.
	version, vendor, ok = parseFullIdentifier("testtool", "3.9.14-apache")
	if !ok {
		t.Fatal("expected an explicit vendor suffix to also still work for a single-vendor tool")
	}
	if version != "3.9.14" || vendor != "apache" {
		t.Errorf("expected version=3.9.14 vendor=apache, got version=%q vendor=%q", version, vendor)
	}
}

// fakeProvider is a minimal registry.Provider stub for tests that
// need a real provider registered under a temporary tool name,
// without depending on any real vendor's network behavior.
type fakeProvider struct{ name string }

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) ResolveAsset(ctx context.Context, version, osName, arch string) (registry.Asset, error) {
	return registry.Asset{}, nil
}
func (f *fakeProvider) ListPatchVersions(ctx context.Context, major, osName, arch string) ([]string, error) {
	return nil, nil
}
func (f *fakeProvider) ListMajorVersions(ctx context.Context) ([]string, error) { return nil, nil }
func (f *fakeProvider) ListMajorVersionsWithLTS(ctx context.Context) ([]registry.MajorVersionInfo, error) {
	return nil, nil
}

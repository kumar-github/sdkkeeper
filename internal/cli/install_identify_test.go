package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sdkkeeper/internal/registry"
	"sdkkeeper/internal/term"
)

// fakePickerProvider is a registry.Provider whose ListMajorVersions/
// ListPatchVersions are fully scripted, for testing
// resolveVendorAndVersion's control flow without any real network
// access.
type fakePickerProvider struct {
	name   string
	majors []string
	// patchesByMajor maps a major to its real patch list. A major
	// with no entry here means ListPatchVersions returns
	// registry.ErrVersionNotFound for it, matching a real vendor 404
	// (e.g. Temurin having no darwin/amd64 build for that major).
	patchesByMajor map[string][]string
	// unrelatedErrMajors, if a major is in this set, ListPatchVersions
	// returns a DIFFERENT error for it (not ErrVersionNotFound) --
	// for confirming that case is handled the same way (fails
	// immediately) as the dead-end case, not specially.
	unrelatedErrMajors map[string]bool
	majorsErr          error

	// queriedMajors records every major ListPatchVersions was called
	// with, in call order -- lets a test assert exactly which majors
	// were actually queried.
	queriedMajors []string
}

func (f *fakePickerProvider) Name() string { return f.name }
func (f *fakePickerProvider) ResolveAsset(ctx context.Context, version, osName, arch string) (registry.Asset, error) {
	return registry.Asset{}, nil
}
func (f *fakePickerProvider) ListMajorVersions(ctx context.Context) ([]string, error) {
	if f.majorsErr != nil {
		return nil, f.majorsErr
	}
	return f.majors, nil
}
func (f *fakePickerProvider) ListMajorVersionsWithLTS(ctx context.Context) ([]registry.MajorVersionInfo, error) {
	return nil, nil
}
func (f *fakePickerProvider) ListPatchVersions(ctx context.Context, major, osName, arch string) ([]string, error) {
	f.queriedMajors = append(f.queriedMajors, major)
	if f.unrelatedErrMajors[major] {
		return nil, errors.New("simulated transport failure")
	}
	patches, ok := f.patchesByMajor[major]
	if !ok {
		return nil, registry.ErrVersionNotFound
	}
	return patches, nil
}

// scriptedPicker returns a runPicker-compatible function that answers
// each call, in order, from responses -- failing the test loudly if
// called more times than scripted (catching an unexpected extra
// picker call, e.g. a regression that re-introduces looping back)
// rather than silently panicking on an out-of-range index.
func scriptedPicker(t *testing.T, responses []string) func(sess *term.Session, title string, items []string, banner string) (string, error) {
	t.Helper()
	i := 0
	return func(sess *term.Session, title string, items []string, banner string) (string, error) {
		if i >= len(responses) {
			t.Fatalf("picker called more times than scripted (call #%d, title=%q, items=%v)", i+1, title, items)
		}
		resp := responses[i]
		i++
		return resp, nil
	}
}

// withScriptedPicker swaps runPicker for a scripted fake for the
// duration of the test, restoring the real picker.Run afterward.
func withScriptedPicker(t *testing.T, responses []string) {
	t.Helper()
	original := runPicker
	runPicker = scriptedPicker(t, responses)
	t.Cleanup(func() { runPicker = original })
}

func TestResolveVendorAndVersion_SingleVendorHappyPath(t *testing.T) {
	setTestHome(t, t.TempDir())
	provider := &fakePickerProvider{
		name:           "apache",
		majors:         []string{"3"},
		patchesByMajor: map[string][]string{"3": {"3.9.9"}},
	}
	withTempProvider(t, "maven", provider)
	withScriptedPicker(t, []string{"3", "3.9.9"}) // major, then patch -- no vendor picker for single-vendor

	tool := mustTool(t, "maven")
	var gotProvider registry.Provider
	var gotVersion string
	var runErr error
	withSession(t, func() {
		gotProvider, gotVersion, runErr = resolveVendorAndVersion(context.Background(), tool)
	})
	if runErr != nil {
		t.Fatalf("expected success, got: %v", runErr)
	}
	if gotVersion != "3.9.9" || gotProvider.Name() != "apache" {
		t.Errorf("expected (apache, 3.9.9), got (%s, %s)", gotProvider.Name(), gotVersion)
	}
	if len(provider.queriedMajors) != 1 || provider.queriedMajors[0] != "3" {
		t.Errorf("expected exactly one ListPatchVersions(3) call, got %v", provider.queriedMajors)
	}
}

func TestResolveVendorAndVersion_MultiVendorHappyPath(t *testing.T) {
	setTestHome(t, t.TempDir())
	provider := &fakePickerProvider{
		name:           "temurin",
		majors:         []string{"21"},
		patchesByMajor: map[string][]string{"21": {"21.0.2-temurin"}},
	}
	liberica := &fakePickerProvider{name: "liberica"} // never queried -- just makes java genuinely multi-vendor
	withTempProviders(t, "java", provider, liberica)
	// Vendor picker shows CAPITALIZED display names -- "Temurin", not
	// "temurin" -- confirming that mapping is correct.
	withScriptedPicker(t, []string{"Temurin", "21", "21.0.2-temurin"})

	tool := mustTool(t, "java")
	var gotProvider registry.Provider
	var gotVersion string
	var runErr error
	withSession(t, func() {
		gotProvider, gotVersion, runErr = resolveVendorAndVersion(context.Background(), tool)
	})
	if runErr != nil {
		t.Fatalf("expected success, got: %v", runErr)
	}
	if gotVersion != "21.0.2-temurin" || gotProvider.Name() != "temurin" {
		t.Errorf("expected (temurin, 21.0.2-temurin), got (%s, %s)", gotProvider.Name(), gotVersion)
	}
}

// TestResolveVendorAndVersion_DeadEndMajorFailsWithoutReshowingPicker
// is the regression test for the real bug report: a major (here "27")
// that ListMajorVersions offers but ListPatchVersions 404s on for
// this platform must fail the whole `sk install` call outright, with
// a clear warning naming the exact platform/vendor/major -- and must
// NOT re-show the major picker. That loop-back approach was tried and
// deliberately reverted: it required carrying the warning as the next
// picker call's own banner argument to avoid a real bug (a plain
// print right before an alt-screen picker launches gets visually
// wiped out -- see picker.Run's own doc comment), and the added
// complexity (banner-threading, mutating the majors list across
// iterations) wasn't worth what it bought, since a fresh
// `sk install <tool>` run already re-shows the full picker if the
// user wants to keep trying.
func TestResolveVendorAndVersion_DeadEndMajorFailsWithoutReshowingPicker(t *testing.T) {
	setTestHome(t, t.TempDir())
	provider := &fakePickerProvider{
		name:   "temurin",
		majors: []string{"27", "21"},
		// "27" has no entry -> ErrVersionNotFound, matching the real
		// darwin/amd64 404 that started this.
		patchesByMajor: map[string][]string{"21": {"21.0.2-temurin"}},
	}
	liberica := &fakePickerProvider{name: "liberica"} // never queried -- just makes java genuinely multi-vendor
	withTempProviders(t, "java", provider, liberica)
	// Only 2 responses -- vendor, then major "27". If the flow
	// incorrectly re-showed the major picker, scriptedPicker itself
	// would fail the test for being called a third time.
	withScriptedPicker(t, []string{"Temurin", "27"})

	tool := mustTool(t, "java")
	var runErr error
	out := withSession(t, func() {
		_, _, runErr = resolveVendorAndVersion(context.Background(), tool)
	})
	if runErr == nil {
		t.Fatal("expected an error -- the chosen major has no releases for this platform")
	}
	if !strings.Contains(out, "No JDK 27 releases for") ||
		!strings.Contains(out, "via temurin") ||
		!strings.Contains(out, "pick a different major version") {
		t.Errorf("expected the dead-end warning with its exact wording, got: %q", out)
	}
	if len(provider.queriedMajors) != 1 || provider.queriedMajors[0] != "27" {
		t.Errorf("expected exactly one query for 27, no retry, got %v", provider.queriedMajors)
	}
}

func TestResolveVendorAndVersion_VendorPickerCancelledReturnsCleanly(t *testing.T) {
	setTestHome(t, t.TempDir())
	provider := &fakePickerProvider{name: "temurin", majors: []string{"21"}}
	liberica := &fakePickerProvider{name: "liberica"} // never queried -- just makes java genuinely multi-vendor
	withTempProviders(t, "java", provider, liberica)
	// Only ONE scripted response -- if the flow incorrectly proceeded
	// to a major picker after cancellation, scriptedPicker itself
	// would fail the test.
	withScriptedPicker(t, []string{""})

	tool := mustTool(t, "java")
	var runErr error
	withSession(t, func() {
		_, _, runErr = resolveVendorAndVersion(context.Background(), tool)
	})
	if runErr == nil {
		t.Fatal("expected an error when the vendor picker is cancelled")
	}
	if len(provider.queriedMajors) != 0 {
		t.Errorf("expected ListPatchVersions never called, got %v", provider.queriedMajors)
	}
}

func TestResolveVendorAndVersion_MajorPickerCancelledReturnsCleanly(t *testing.T) {
	setTestHome(t, t.TempDir())
	provider := &fakePickerProvider{name: "apache", majors: []string{"3"}}
	withTempProvider(t, "maven", provider)
	withScriptedPicker(t, []string{""}) // single-vendor -- this IS the major picker

	tool := mustTool(t, "maven")
	var runErr error
	withSession(t, func() {
		_, _, runErr = resolveVendorAndVersion(context.Background(), tool)
	})
	if runErr == nil {
		t.Fatal("expected an error when the major picker is cancelled")
	}
	if len(provider.queriedMajors) != 0 {
		t.Errorf("expected ListPatchVersions never called, got %v", provider.queriedMajors)
	}
}

func TestResolveVendorAndVersion_PatchPickerCancelledReturnsCleanly(t *testing.T) {
	setTestHome(t, t.TempDir())
	provider := &fakePickerProvider{
		name:           "apache",
		majors:         []string{"3"},
		patchesByMajor: map[string][]string{"3": {"3.9.9"}},
	}
	withTempProvider(t, "maven", provider)
	withScriptedPicker(t, []string{"3", ""}) // major succeeds, then patch picker cancelled

	tool := mustTool(t, "maven")
	var runErr error
	withSession(t, func() {
		_, _, runErr = resolveVendorAndVersion(context.Background(), tool)
	})
	if runErr == nil {
		t.Fatal("expected an error when the patch picker is cancelled")
	}
	if len(provider.queriedMajors) != 1 {
		t.Errorf("expected exactly 1 query, got %v", provider.queriedMajors)
	}
}

// TestResolveVendorAndVersion_UnrelatedPatchListErrorFailsImmediately
// confirms a NON-ErrVersionNotFound failure from ListPatchVersions
// (e.g. a real transport error) is reported with its own distinct
// message, not the dead-end wording -- it isn't evidence the major
// has no releases, just that this one call failed.
func TestResolveVendorAndVersion_UnrelatedPatchListErrorFailsImmediately(t *testing.T) {
	setTestHome(t, t.TempDir())
	provider := &fakePickerProvider{
		name:               "apache",
		majors:             []string{"3", "17"},
		unrelatedErrMajors: map[string]bool{"3": true},
	}
	withTempProvider(t, "maven", provider)
	withScriptedPicker(t, []string{"3"})

	tool := mustTool(t, "maven")
	var runErr error
	out := withSession(t, func() {
		_, _, runErr = resolveVendorAndVersion(context.Background(), tool)
	})
	if runErr == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(out, "pick a different major version") {
		t.Errorf("expected the distinct 'could not list versions' message, not the dead-end wording, got: %q", out)
	}
	if len(provider.queriedMajors) != 1 {
		t.Errorf("expected exactly 1 query, got %v", provider.queriedMajors)
	}
}

func TestResolveVendorAndVersion_ListMajorVersionsErrorFailsCleanly(t *testing.T) {
	setTestHome(t, t.TempDir())
	provider := &fakePickerProvider{name: "apache", majorsErr: errors.New("simulated network failure")}
	withTempProvider(t, "maven", provider)
	withScriptedPicker(t, []string{}) // no picker call should happen at all

	tool := mustTool(t, "maven")
	var runErr error
	withSession(t, func() {
		_, _, runErr = resolveVendorAndVersion(context.Background(), tool)
	})
	if runErr == nil {
		t.Fatal("expected an error")
	}
}

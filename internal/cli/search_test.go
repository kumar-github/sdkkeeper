package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sdkkeeper/internal/inventory"
	"sdkkeeper/internal/registry"
	"sdkkeeper/internal/term"
	"sdkkeeper/internal/tooldef"
)

func TestCapitalize(t *testing.T) {
	cases := map[string]string{
		"temurin":  "Temurin",
		"liberica": "Liberica",
		"":         "",
		"a":        "A",
	}
	for in, want := range cases {
		got := capitalize(in)
		if got != want {
			t.Errorf("capitalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInstalledMarker_RealDirectory(t *testing.T) {
	v := inventory.Version{Number: "21.0.2-temurin", External: false}
	got := installedMarker(v)
	if got != "\u2713 installed" {
		t.Errorf("expected plain 'installed' marker for a real, sk-installed entry, got: %q", got)
	}
}

func TestInstalledMarker_Symlink(t *testing.T) {
	v := inventory.Version{Number: "11.0.15", External: true}
	got := installedMarker(v)
	// Reuses the exact phrase already established in list.go's own
	// output for this same distinction -- immediately recognizable
	// rather than new wording.
	want := "\u2713 installed (not managed by SDK Keeper)"
	if got != want {
		t.Errorf("expected the 'not managed by SDK Keeper' qualifier for a symlinked entry, got: %q", got)
	}
}

// mockProvider is a minimal, hand-built registry.Provider for testing
// searchMajors/searchPatches against known data, without needing real
// network access to a real vendor API (unavailable in this sandbox --
// see the real vendor tests in internal/registry/* for that level of
// coverage instead).
type mockProvider struct {
	name    string
	majors  []registry.MajorVersionInfo
	patches []string
}

func (m mockProvider) Name() string { return m.name }
func (m mockProvider) ResolveAsset(ctx context.Context, version, osName, arch string) (registry.Asset, error) {
	return registry.Asset{}, registry.ErrVersionNotFound
}
func (m mockProvider) ListPatchVersions(ctx context.Context, major, osName, arch string) ([]string, error) {
	if len(m.patches) == 0 {
		return nil, registry.ErrVersionNotFound
	}
	return m.patches, nil
}
func (m mockProvider) ListMajorVersions(ctx context.Context) ([]string, error) {
	out := make([]string, len(m.majors))
	for i, info := range m.majors {
		out[i] = info.Number
	}
	return out, nil
}
func (m mockProvider) ListMajorVersionsWithLTS(ctx context.Context) ([]registry.MajorVersionInfo, error) {
	return m.majors, nil
}

// withSession temporarily sets the package-level session/styles vars
// (normally set up by root.go's PreRunE against a real terminal) to a
// pipe-backed session for the duration of fn, capturing everything
// written to session.Out -- mirrors captureStdout's own real os.Pipe
// technique, applied to session.Out specifically since these
// functions write there, not to bare os.Stdout.
func withSession(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	oldSession, oldStyles := session, styles
	session = &term.Session{Out: w}
	styles = term.StylesForWriter(w)

	fn()

	w.Close()
	session, styles = oldSession, oldStyles

	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String()
}

func TestSearchMajors_NoTruncationAndLTSLabelPresent(t *testing.T) {
	tool, _ := tooldef.Get("java")
	provider := mockProvider{
		name: "temurin",
		majors: []registry.MajorVersionInfo{
			{Number: "21", LTS: true},
			{Number: "20", LTS: false},
			{Number: "8", LTS: true},
		},
	}
	out := withSession(t, func() {
		if err := searchMajors(context.Background(), tool, provider, "Temurin"); err != nil {
			t.Fatalf("searchMajors failed: %v", err)
		}
	})
	for _, want := range []string{"21", "20", "8", "LTS"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got:\n%s", want, out)
		}
	}
}

func TestSearchPatches_NoTruncationAndInstalledMarkerPresent(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	tool, _ := tooldef.Get("java")

	candidateDir := filepath.Join(home, ".sdkkeeper", "candidates", "java", "JDK-25.0.1-liberica")
	if err := os.MkdirAll(candidateDir, 0o755); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	provider := mockProvider{
		name:    "liberica",
		patches: []string{"25.0.4.1", "25.0.4", "25.0.1", "25"},
	}
	out := withSession(t, func() {
		if err := searchPatches(context.Background(), tool, provider, "Liberica", "25"); err != nil {
			t.Fatalf("searchPatches failed: %v", err)
		}
	})
	for _, want := range []string{"25.0.4.1", "25.0.4", "25.0.1", "25", "installed"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got:\n%s", want, out)
		}
	}
}

func TestSearchPatches_NoReleasesFoundReportsClearly(t *testing.T) {
	tool, _ := tooldef.Get("java")
	provider := mockProvider{name: "temurin"} // no patches configured
	out := withSession(t, func() {
		err := searchPatches(context.Background(), tool, provider, "Temurin", "99")
		if err == nil {
			t.Error("expected an error when no releases are found")
		}
	})
	if !strings.Contains(out, "No JDK 99 releases found") {
		t.Errorf("expected a clear 'no releases found' message, got:\n%s", out)
	}
}

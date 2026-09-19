package cli

import (
	"os"
	"path/filepath"
	"testing"

	"sdkkeeper/internal/tooldef"
)

func TestResolveAddJSON_Errors(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")

	file := filepath.Join(home, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("test setup failed: %v", err)
	}

	cases := []struct {
		name    string
		tool    string
		version string
		source  string
		want    ErrorCode
	}{
		{"unknown tool", "not-a-real-tool", "1.0", t.TempDir(), ErrCodeAmbiguousTool},
		{"nonexistent source", "java", "21.0.2", filepath.Join(home, "does-not-exist"), ErrCodeInvalidPath},
		{"source is a file", "java", "21.0.2", file, ErrCodeInvalidPath},
		{"already registered", "java", "21.0.2-temurin", t.TempDir(), ErrCodeAlreadyRegistered},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, jerr := resolveAddJSON(tc.tool, tc.version, tc.source)
			if jerr == nil || jerr.Code != tc.want {
				t.Fatalf("expected %s, got: %+v", tc.want, jerr)
			}
		})
	}
}

// TestResolveAddJSON_RegistersSuccessfully covers the happy path: a
// real source directory is symlinked in and the payload reports it
// correctly.
func TestResolveAddJSON_RegistersSuccessfully(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	source := t.TempDir()

	data, jerr := resolveAddJSON("java", "21.0.2-temurin", source)
	if jerr != nil {
		t.Fatalf("expected success, got error: %+v", jerr)
	}
	if data.Action != string(actionAdded) || data.Tool != "java" || data.Version != "21.0.2-temurin" {
		t.Errorf("unexpected payload: %+v", data)
	}
	if data.Vendor == nil || *data.Vendor != "temurin" {
		t.Errorf("expected vendor=temurin, got %v", data.Vendor)
	}

	tool, _ := tooldef.Get("java")
	wantPath := filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+"21.0.2-temurin")
	if data.Path != wantPath {
		t.Errorf("expected path=%q, got %q", wantPath, data.Path)
	}
	real, err := os.Readlink(wantPath)
	if err != nil || real != source {
		t.Errorf("expected a symlink at %s pointing to %s, got %q (err: %v)", wantPath, source, real, err)
	}
}

func TestResolveAddJSON_SingleVendorToolHasNilVendor(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	data, jerr := resolveAddJSON("maven", "3.9.9", t.TempDir())
	if jerr != nil {
		t.Fatalf("expected success, got error: %+v", jerr)
	}
	if data.Vendor != nil {
		t.Errorf("expected a nil vendor for single-vendor maven, got %q", *data.Vendor)
	}
}

// TestResolveAddJSON_RelativeSourceResolvesToAbsolute guards a real
// bug shape: a relative source arg must still be baked into the
// symlink as an absolute path.
func TestResolveAddJSON_RelativeSourceResolvesToAbsolute(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	source := t.TempDir()

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("could not get working directory: %v", err)
	}
	defer os.Chdir(oldWd)
	if err := os.Chdir(filepath.Dir(source)); err != nil {
		t.Fatalf("could not chdir: %v", err)
	}

	data, jerr := resolveAddJSON("java", "21.0.2-temurin", filepath.Base(source))
	if jerr != nil {
		t.Fatalf("expected success, got error: %+v", jerr)
	}
	real, err := os.Readlink(data.Path)
	if err != nil || !filepath.IsAbs(real) {
		t.Errorf("expected an absolute symlink target, got %q (err: %v)", real, err)
	}
}

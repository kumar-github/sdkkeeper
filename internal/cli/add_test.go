package cli

import (
	"os"
	"path/filepath"
	"testing"

	"sdkkeeper/internal/tooldef"
)

func TestResolveAddJSON_UnknownToolIsAmbiguousTool(t *testing.T) {
	setTestHome(t, t.TempDir())
	_, jerr := resolveAddJSON("not-a-real-tool", "1.0", t.TempDir())
	if jerr == nil || jerr.Code != ErrCodeAmbiguousTool {
		t.Fatalf("expected ambiguous_tool, got: %+v", jerr)
	}
}

// TestResolveAddJSON_NonexistentPathIsInvalidPath confirms a source
// path that doesn't exist is invalid_path, not internal_error --
// mirrors the interactive RunE's own os.Stat check.
func TestResolveAddJSON_NonexistentPathIsInvalidPath(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	_, jerr := resolveAddJSON("java", "21.0.2", filepath.Join(home, "does-not-exist"))
	if jerr == nil || jerr.Code != ErrCodeInvalidPath {
		t.Fatalf("expected invalid_path for a nonexistent source, got: %+v", jerr)
	}
}

// TestResolveAddJSON_FileNotDirectoryIsInvalidPath confirms a source
// path that exists but is a plain file (not a directory) is also
// invalid_path.
func TestResolveAddJSON_FileNotDirectoryIsInvalidPath(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	file := filepath.Join(home, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("test setup failed: %v", err)
	}
	_, jerr := resolveAddJSON("java", "21.0.2", file)
	if jerr == nil || jerr.Code != ErrCodeInvalidPath {
		t.Fatalf("expected invalid_path for a non-directory source, got: %+v", jerr)
	}
}

// TestResolveAddJSON_AlreadyRegisteredIsAlreadyRegistered confirms a
// version slot that's already occupied (real dir or symlink) reports
// the new, dedicated already_registered code, distinct from install's
// own already_installed for the same underlying "slot taken" shape.
func TestResolveAddJSON_AlreadyRegisteredIsAlreadyRegistered(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	installFakeJava(t, home, "21.0.2-temurin")

	source := t.TempDir()
	_, jerr := resolveAddJSON("java", "21.0.2-temurin", source)
	if jerr == nil || jerr.Code != ErrCodeAlreadyRegistered {
		t.Fatalf("expected already_registered, got: %+v", jerr)
	}
}

// TestResolveAddJSON_RealDirectoryRegistersSuccessfully is the happy
// path: a real, valid source directory is symlinked in, and the
// returned payload correctly reports it -- confirmed against the real
// filesystem, not just the returned struct.
func TestResolveAddJSON_RealDirectoryRegistersSuccessfully(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	source := t.TempDir()

	data, jerr := resolveAddJSON("java", "21.0.2-temurin", source)
	if jerr != nil {
		t.Fatalf("expected a successful add, got error: %+v", jerr)
	}
	if data.Action != string(actionAdded) {
		t.Errorf("expected action=added, got %q", data.Action)
	}
	if data.Tool != "java" || data.Version != "21.0.2-temurin" {
		t.Errorf("expected tool=java version=21.0.2-temurin, got tool=%q version=%q", data.Tool, data.Version)
	}
	if data.Vendor == nil || *data.Vendor != "temurin" {
		t.Errorf("expected vendor=temurin (parsed from the version suffix), got %v", data.Vendor)
	}

	tool, _ := tooldef.Get("java")
	wantPath := filepath.Join(tool.CandidateRoot(), tool.FolderPrefix+"21.0.2-temurin")
	if data.Path != wantPath {
		t.Errorf("expected path=%q, got %q", wantPath, data.Path)
	}

	real, err := os.Readlink(wantPath)
	if err != nil {
		t.Fatalf("expected a real symlink at %s: %v", wantPath, err)
	}
	if real != source {
		t.Errorf("expected symlink to point at %s, got %s", source, real)
	}
}

// TestResolveAddJSON_SingleVendorToolHasNilVendor confirms a
// single-vendor tool (Maven) reports vendor=null, matching vendorOf's
// existing convention used by use/remove/list.
func TestResolveAddJSON_SingleVendorToolHasNilVendor(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	source := t.TempDir()

	data, jerr := resolveAddJSON("maven", "3.9.9", source)
	if jerr != nil {
		t.Fatalf("expected a successful add, got error: %+v", jerr)
	}
	if data.Vendor != nil {
		t.Errorf("expected a nil vendor for single-vendor maven, got %q", *data.Vendor)
	}
}

// TestResolveAddJSON_RelativePathIsResolvedToAbsolute confirms a
// relative source path argument is still resolved to an absolute path
// before being baked into the symlink -- see the interactive RunE's
// own comment on why a relative path would otherwise resolve wrong
// when later followed.
func TestResolveAddJSON_RelativePathIsResolvedToAbsolute(t *testing.T) {
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
		t.Fatalf("expected a successful add, got error: %+v", jerr)
	}

	real, err := os.Readlink(data.Path)
	if err != nil {
		t.Fatalf("expected a real symlink at %s: %v", data.Path, err)
	}
	if !filepath.IsAbs(real) {
		t.Errorf("expected the symlink target to be an absolute path, got %q", real)
	}
}

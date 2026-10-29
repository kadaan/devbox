package shellgen

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.jetify.com/devbox/internal/devbox/devopt"
	"go.jetify.com/devbox/internal/devconfig/configfile"
	"go.jetify.com/devbox/internal/devpkg"
	"go.jetify.com/devbox/internal/lock"
	"go.jetify.com/devbox/nix/flake"
)

type lockMock struct{}

func (l *lockMock) Get(key string) *lock.Package {
	return nil
}

func (l *lockMock) Stdenv() flake.Ref {
	return flake.Ref{}
}

func (l *lockMock) ProjectDir() string {
	return ""
}

func (l *lockMock) Resolve(key string) (*lock.Package, error) {
	if strings.HasPrefix(key, "runx:") {
		return &lock.Package{Resolved: key}, nil
	}
	return &lock.Package{
		Resolved: "github:NixOS/nixpkgs/10b813040df67c4039086db0f6eaf65c536886c6#python312",
	}, nil
}

func TestNewGlibcPatchFlake(t *testing.T) {
	stdenv := flake.Ref{
		Type: flake.TypeGitHub,
		URL:  "https://github.com/NixOS/nixpkgs",
		Ref:  "nixpkgs-unstable",
	}

	packages := devpkg.PackagesFromStringsWithOptions([]string{"python@latest"}, &lockMock{}, devopt.AddOpts{
		Patch: string(configfile.PatchAlways),
	})

	patchFlake, err := newGlibcPatchFlake(stdenv, packages)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if patchFlake.NixpkgsGlibcFlakeRef != stdenv.String() {
		t.Errorf("expected NixpkgsGlibcFlakeRef to be %s, got %s", stdenv.String(), patchFlake.NixpkgsGlibcFlakeRef)
	}

	if len(patchFlake.Outputs.Packages) != 1 {
		t.Errorf("expected 1 package in Outputs, got %d", len(patchFlake.Outputs.Packages))
	}
}

func TestDevboxPatchFlakeRef(t *testing.T) {
	const commit = "597c2f6f3bb0319d08780df0fa3cd257405bd3ff"
	noSource := func() (string, error) { return "", errors.New("no source dir") }

	t.Run("local source wins", func(t *testing.T) {
		ref := devboxPatchFlakeRef(commit, func() (string, error) { return "/src/devbox", nil })
		if got, want := ref.String(), "path:/src/devbox"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("falls back to the commit", func(t *testing.T) {
		ref := devboxPatchFlakeRef(commit, noSource)
		if got, want := ref.String(), "github:kadaan/devbox/"+commit; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	// A `git describe` string is what broke this: GitHub reads an unresolvable
	// ref as a SHA and answers 422, failing every shell whose project holds a
	// package that needs patching.
	t.Run("never emits a ref GitHub cannot resolve", func(t *testing.T) {
		for _, notACommit := range []string{
			"none", "0.0.0-dev", "0.18.0-ens001-1-g597c2f6f", "0.18.0-ens001-1-g597c2f6f-dirty",
			"597c2f6f", strings.ToUpper(commit),
		} {
			ref := devboxPatchFlakeRef(notACommit, noSource)
			if ref.Ref != "" {
				t.Errorf("%q was used as a git ref, got %q", notACommit, ref.String())
			}
			if got, want := ref.String(), "github:kadaan/devbox"; got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		}
	})
}

func TestNewGlibcPatchFlakeSkipsNonNix(t *testing.T) {
	stdenv := flake.Ref{
		Type: flake.TypeGitHub,
		URL:  "https://github.com/NixOS/nixpkgs",
		Ref:  "nixpkgs-unstable",
	}
	packages := devpkg.PackagesFromStringsWithOptions(
		[]string{"python@latest", "runx:golangci/golangci-lint@latest"},
		&lockMock{},
		devopt.AddOpts{Patch: string(configfile.PatchAlways)},
	)

	patchFlake, err := newGlibcPatchFlake(stdenv, packages)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(patchFlake.Inputs) != 1 {
		t.Errorf("expected 1 flake input, got %v", patchFlake.Inputs)
	}
}

func TestFetchClosureStorePathExpr(t *testing.T) {
	got := fetchClosureStorePathExpr("/nix/store/0123456789abcdfghijklmnpqrsvwxyz-python3-3.12.0")
	want := `builtins.fetchClosure {
  fromStore = "https://cache.nixos.org";
  fromPath = "/nix/store/0123456789abcdfghijklmnpqrsvwxyz-python3-3.12.0";
  inputAddressed = true;
}`
	if got != want {
		t.Errorf("got expression:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestCopySystemCUDALibRemovesStaleLibs(t *testing.T) {
	flakeDir := t.TempDir()
	libDir := filepath.Join(flakeDir, "lib")
	if err := os.Mkdir(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(libDir, "libcuda.so.0.0.0-stale")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The result depends on whether the system has a CUDA driver, but
	// either way the stale library should be gone.
	_ = (&glibcPatchFlake{}).copySystemCUDALib(flakeDir)
	if _, err := os.Lstat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("got stale library error %v, want %v", err, fs.ErrNotExist)
	}
}

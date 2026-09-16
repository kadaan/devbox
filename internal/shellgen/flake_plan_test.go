package shellgen

import (
	"errors"
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

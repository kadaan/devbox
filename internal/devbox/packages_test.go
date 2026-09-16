// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package devbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jetify.com/devbox/internal/devbox/devopt"
	"go.jetify.com/devbox/internal/devpkg"
	"go.jetify.com/devbox/internal/lock"
	"go.jetify.com/devbox/internal/nix"
	"go.jetify.com/devbox/nix/flake"
)

const testStoreHashPad = "00000000000000000000000000000000"

type testLocker struct {
	entry *lock.Package
}

func (l *testLocker) ProjectDir() string { return "/tmp/my-project" }

func (l *testLocker) Stdenv() flake.Ref {
	return flake.Ref{Type: flake.TypeGitHub, Owner: "NixOS", Repo: "nixpkgs", Rev: "abc123"}
}

func (l *testLocker) Get(pkg string) *lock.Package { return l.entry }

func (l *testLocker) Resolve(pkg string) (*lock.Package, error) { return l.entry, nil }

func testPackage(systems ...string) *devpkg.Package {
	entry := &lock.Package{
		Resolved: "github:NixOS/nixpkgs/abc123#hello",
		Systems:  map[string]*lock.SystemInfo{},
	}
	for _, system := range systems {
		entry.Systems[system] = &lock.SystemInfo{}
	}
	return devpkg.PackageFromStringWithDefaults("hello@1.0", &testLocker{entry: entry})
}

// otherSystems returns n nix systems that are not the one the test is running on.
func otherSystems(t *testing.T, n int) []string {
	t.Helper()
	systems := []string{}
	for _, system := range []string{"x86_64-linux", "aarch64-linux", "x86_64-darwin", "aarch64-darwin"} {
		if system != nix.System() && len(systems) < n {
			systems = append(systems, system)
		}
	}
	if len(systems) < n {
		t.Fatalf("fewer than %d systems other than %q", n, nix.System())
	}
	return systems
}

func storePath(system, output string) string {
	path := "/nix/store/" + (system + testStoreHashPad)[:32] + "-hello-1.0"
	if output != "" {
		path += "-" + output
	}
	return path
}

func TestOutputsForSystems(t *testing.T) {
	ctx := t.Context()
	systems := otherSystems(t, 2)
	pkg := testPackage(systems...)

	evaluate := func(_ context.Context, installable string, _ bool) ([]string, error) {
		for _, system := range systems {
			if installable == "github:NixOS/nixpkgs/abc123#legacyPackages."+system+".hello" {
				return []string{storePath(system, "")}, nil
			}
		}
		return nil, errors.New("unexpected installable " + installable)
	}

	got, err := outputsForSystems(ctx, pkg, systems, evaluate)
	require.NoError(t, err)

	assert.Equal(t, map[string][]lock.Output{
		systems[0]: {{Path: storePath(systems[0], ""), Default: true}},
		systems[1]: {{Path: storePath(systems[1], ""), Default: true}},
	}, got)
}

func TestOutputsForSystemsSkipsFailedSystem(t *testing.T) {
	ctx := t.Context()
	systems := otherSystems(t, 2)
	good, bad := systems[0], systems[1]
	pkg := testPackage(good, bad)

	evaluate := func(_ context.Context, installable string, _ bool) ([]string, error) {
		if installable == "github:NixOS/nixpkgs/abc123#legacyPackages."+bad+".hello" {
			return nil, errors.New("assertion failed")
		}
		return []string{storePath(good, "")}, nil
	}

	got, err := outputsForSystems(ctx, pkg, []string{good, bad}, evaluate)
	require.NoError(t, err)

	assert.Len(t, got, 1)
	assert.Equal(t, storePath(good, ""), got[good][0].Path)
}

func TestOutputsForSystemsSkipsSystemWithNoStorePaths(t *testing.T) {
	ctx := t.Context()
	system := otherSystems(t, 1)[0]
	pkg := testPackage(system)

	evaluate := func(_ context.Context, _ string, _ bool) ([]string, error) {
		return nil, nil
	}

	got, err := outputsForSystems(ctx, pkg, []string{system}, evaluate)
	require.NoError(t, err)

	assert.Empty(t, got)
}

func TestOutputsForSystemsNamesOutputs(t *testing.T) {
	ctx := t.Context()
	pkg := testPackage(nix.System())

	evaluate := func(_ context.Context, _ string, _ bool) ([]string, error) {
		return []string{storePath(nix.System(), "man")}, nil
	}

	got, err := outputsForSystems(ctx, pkg, []string{nix.System()}, evaluate)
	require.NoError(t, err)

	assert.Equal(t, "man", got[nix.System()][0].Name)
}

func TestOutputsForSystemsFailsOnHostSystem(t *testing.T) {
	ctx := t.Context()
	other := otherSystems(t, 1)[0]
	pkg := testPackage(nix.System(), other)

	evaluate := func(_ context.Context, installable string, _ bool) ([]string, error) {
		if strings.Contains(installable, nix.System()) || !strings.Contains(installable, other) {
			return nil, errors.New("broken build")
		}
		return []string{storePath(other, "")}, nil
	}

	got, err := outputsForSystems(ctx, pkg, []string{nix.System(), other}, evaluate)

	require.Error(t, err)
	assert.Nil(t, got)
}

func lockedOutputs(system string) []lock.Output {
	return []lock.Output{{Name: "out", Path: storePath(system, ""), Default: true}}
}

// lockedEntry locks pkg for every store-path system except skip.
func lockedEntry(pkg, skip string) *lock.Package {
	entry := &lock.Package{
		Resolved: "github:NixOS/nixpkgs/abc123#" + pkg,
		Version:  "1.0",
		Source:   "devbox-search",
		Systems:  map[string]*lock.SystemInfo{},
	}
	for _, system := range []string{"aarch64-darwin", "aarch64-linux", "x86_64-darwin", "x86_64-linux"} {
		if system != skip {
			entry.Systems[system] = &lock.SystemInfo{Outputs: lockedOutputs(system)}
		}
	}
	return entry
}

func devboxWithLockfile(t *testing.T, packages []string, entries map[string]*lock.Package) *Devbox {
	t.Helper()
	dir := t.TempDir()

	cfg, err := json.Marshal(map[string]any{"packages": packages})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "devbox.json"), cfg, 0o644))

	lockfile, err := json.Marshal(map[string]any{"lockfile_version": "1", "packages": entries})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "devbox.lock"), lockfile, 0o644))

	d, err := Open(&devopt.Opts{Dir: dir, Stderr: os.Stderr})
	require.NoError(t, err)
	return d
}

func TestFixMissingStorePathsSavesWorkDoneBeforeAFailure(t *testing.T) {
	other := otherSystems(t, 1)[0]
	box := devboxWithLockfile(t,
		[]string{"good@1.0", "bad@1.0"},
		map[string]*lock.Package{
			"good@1.0": lockedEntry("good", other),
			"bad@1.0":  lockedEntry("bad", nix.System()),
		},
	)

	evaluate := func(_ context.Context, installable string, _ bool) ([]string, error) {
		if strings.Contains(installable, "#bad") {
			return nil, errors.New("broken build")
		}
		return []string{storePath(other, "")}, nil
	}

	err := box.fixMissingStorePaths(t.Context(), evaluate)
	require.Error(t, err)

	saved := &lock.File{}
	contents, err := os.ReadFile(filepath.Join(box.ProjectDir(), "devbox.lock"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(contents, saved))
	require.Contains(t, saved.Packages, "good@1.0")
	require.Contains(t, saved.Packages["good@1.0"].Systems, other)
	assert.Equal(t,
		[]lock.Output{{Path: storePath(other, ""), Default: true}},
		saved.Packages["good@1.0"].Systems[other].Outputs,
	)
}

// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package devpkg

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jetify.com/devbox/internal/devbox/devopt"
	"go.jetify.com/devbox/internal/lock"
	"go.jetify.com/devbox/internal/nix"
	"go.jetify.com/devbox/nix/flake"
)

// entryLocker resolves every package to one fixed lockfile entry.
type entryLocker struct {
	entry *lock.Package
}

func (l *entryLocker) ProjectDir() string { return "/tmp/my-project" }

func (l *entryLocker) Stdenv() flake.Ref {
	return flake.Ref{Type: flake.TypeGitHub, Owner: "NixOS", Repo: "nixpkgs", Rev: nixCommitHash}
}

func (l *entryLocker) Get(pkg string) *lock.Package { return l.entry }

func (l *entryLocker) Resolve(pkg string) (*lock.Package, error) { return l.entry, nil }

// otherSystem returns a nix system that is not the one the test is running on.
func otherSystem(t *testing.T) string {
	t.Helper()
	for _, system := range []string{"x86_64-linux", "aarch64-darwin"} {
		if system != nix.System() {
			return system
		}
	}
	t.Fatalf("no system other than %q", nix.System())
	return ""
}

func TestInstallablesForSystem(t *testing.T) {
	other := otherSystem(t)
	tests := []struct {
		name     string
		resolved string
		outputs  []string
		want     []string
	}{
		{
			name:     "qualifies a bare attr path",
			resolved: "github:NixOS/nixpkgs/abc123#hello",
			want:     []string{"github:NixOS/nixpkgs/abc123#legacyPackages." + other + ".hello"},
		},
		{
			name:     "keeps the output selection",
			resolved: "github:NixOS/nixpkgs/abc123#curl",
			outputs:  []string{"out", "man"},
			want:     []string{"github:NixOS/nixpkgs/abc123#legacyPackages." + other + ".curl^man,out"},
		},
		{
			name:     "re-qualifies an attr path baked for another system",
			resolved: "github:NixOS/nixpkgs/abc123#legacyPackages.i686-linux.hello",
			want:     []string{"github:NixOS/nixpkgs/abc123#legacyPackages." + other + ".hello"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pkg := PackageFromStringWithOptions(
				"hello@1.0",
				&entryLocker{entry: &lock.Package{Resolved: test.resolved}},
				devopt.AddOpts{Outputs: test.outputs},
			)
			got, err := pkg.InstallablesForSystem(other)
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestInstallablesForSystemMatchesHostInstallables(t *testing.T) {
	pkg := PackageFromStringWithDefaults(
		"hello@1.0",
		&entryLocker{entry: &lock.Package{Resolved: "github:NixOS/nixpkgs/abc123#hello"}},
	)

	want, err := pkg.Installables()
	require.NoError(t, err)
	got, err := pkg.InstallablesForSystem(nix.System())
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// allStorePathSystems returns every system a tidy run fills store paths for.
func allStorePathSystems(t *testing.T) []string {
	t.Helper()
	systems := []string{"aarch64-darwin", "aarch64-linux", "x86_64-darwin", "x86_64-linux"}
	if !slices.Contains(systems, nix.System()) {
		systems = append(systems, nix.System())
	}
	return systems
}

func withoutSystem(systems []string, drop string) []string {
	kept := []string{}
	for _, system := range systems {
		if system != drop {
			kept = append(kept, system)
		}
	}
	return kept
}

func lockedSystems(systems []string, outputs []lock.Output) map[string]*lock.SystemInfo {
	locked := map[string]*lock.SystemInfo{}
	for _, system := range systems {
		locked[system] = &lock.SystemInfo{Outputs: outputs}
	}
	return locked
}

func TestSystemsMissingStorePaths(t *testing.T) {
	all := allStorePathSystems(t)
	other := otherSystem(t)
	locked := []lock.Output{{Name: "out", Path: "/nix/store/abc-hello-1.0", Default: true}}
	tests := []struct {
		name    string
		systems map[string]*lock.SystemInfo
		want    []string
	}{
		{
			name: "no systems in the lockfile",
			want: all,
		},
		{
			name: "host locked, other systems empty",
			systems: map[string]*lock.SystemInfo{
				nix.System(): {Outputs: locked},
				other:        {},
			},
			want: withoutSystem(all, nix.System()),
		},
		{
			name: "other system locked, host empty",
			systems: map[string]*lock.SystemInfo{
				nix.System(): {},
				other:        {Outputs: locked},
			},
			want: withoutSystem(all, other),
		},
		{
			name:    "every system locked",
			systems: lockedSystems(all, locked),
			want:    []string{},
		},
		{
			name: "a system outside the default set is reported too",
			systems: map[string]*lock.SystemInfo{
				"i686-linux": {},
			},
			want: append(slices.Clone(all), "i686-linux"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pkg := PackageFromStringWithDefaults(
				"hello@1.0",
				&entryLocker{entry: &lock.Package{
					Resolved: "github:NixOS/nixpkgs/abc123#hello",
					Systems:  test.systems,
				}},
			)

			got, err := pkg.SystemsMissingStorePaths()
			require.NoError(t, err)
			assert.ElementsMatch(t, test.want, got)
		})
	}
}

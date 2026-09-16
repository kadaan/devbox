// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package lock

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jetify.com/devbox/nix/flake"
)

type testProject struct {
	projectDir string
}

func (p *testProject) ConfigHash() (string, error) { return "test-config-hash", nil }

func (p *testProject) Stdenv() flake.Ref {
	return flake.Ref{Type: flake.TypeGitHub, Owner: "NixOS", Repo: "nixpkgs"}
}

func (p *testProject) AllPackageNamesIncludingRemovedTriggerPackages() []string { return nil }

func (p *testProject) ProjectDir() string { return p.projectDir }

func newTestFile(t *testing.T, pkgs map[string]*Package) *File {
	t.Helper()
	return &File{
		devboxProject:   &testProject{projectDir: t.TempDir()},
		LockFileVersion: lockFileVersion,
		Packages:        pkgs,
	}
}

func TestSetOutputsForPackage(t *testing.T) {
	f := newTestFile(t, map[string]*Package{
		"hello@1.0": {Resolved: "github:NixOS/nixpkgs/abc123#hello", Version: "1.0"},
	})

	linuxOutputs := []Output{{Name: "out", Path: "/nix/store/linux-hello-1.0", Default: true}}
	darwinOutputs := []Output{{Name: "out", Path: "/nix/store/darwin-hello-1.0", Default: true}}

	require.NoError(t, f.SetOutputsForPackage("hello@1.0", "x86_64-linux", linuxOutputs))
	require.NoError(t, f.SetOutputsForPackage("hello@1.0", "aarch64-darwin", darwinOutputs))

	systems := f.Packages["hello@1.0"].Systems
	assert.Equal(t, linuxOutputs, systems["x86_64-linux"].Outputs)
	assert.Equal(t, darwinOutputs, systems["aarch64-darwin"].Outputs)
}

func TestSetOutputsForPackagePreservesOtherFields(t *testing.T) {
	existing := []Output{{Name: "out", Path: "/nix/store/existing-hello-1.0", Default: true}}
	lockFile := newTestFile(t, map[string]*Package{
		"hello@1.0": {
			Resolved: "github:NixOS/nixpkgs/abc123#hello",
			Version:  "1.0",
			Systems: map[string]*SystemInfo{
				"aarch64-darwin": {Outputs: existing},
				"x86_64-linux":   {},
			},
		},
	})

	added := []Output{{Name: "out", Path: "/nix/store/linux-hello-1.0", Default: true}}
	require.NoError(t, lockFile.SetOutputsForPackage("hello@1.0", "x86_64-linux", added))

	pkg := lockFile.Packages["hello@1.0"]
	assert.Equal(t, "github:NixOS/nixpkgs/abc123#hello", pkg.Resolved)
	assert.Equal(t, "1.0", pkg.Version)
	assert.Equal(t, existing, pkg.Systems["aarch64-darwin"].Outputs)
	assert.Equal(t, added, pkg.Systems["x86_64-linux"].Outputs)
}

func TestSetOutputsForPackageSaveWritesLockfile(t *testing.T) {
	project := &testProject{projectDir: t.TempDir()}
	f := &File{
		devboxProject:   project,
		LockFileVersion: lockFileVersion,
		Packages: map[string]*Package{
			"hello@1.0": {Resolved: "github:NixOS/nixpkgs/abc123#hello", Version: "1.0"},
		},
	}

	outputs := []Output{{Name: "out", Path: "/nix/store/linux-hello-1.0", Default: true}}
	require.NoError(t, f.SetOutputsForPackage("hello@1.0", "x86_64-linux", outputs))
	require.NoError(t, f.Save())

	saved, err := GetFile(project)
	require.NoError(t, err)
	assert.Equal(t, outputs, saved.Packages["hello@1.0"].Systems["x86_64-linux"].Outputs)
}

// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package searchproxy_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jetify.com/devbox/internal/lock"
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

func TestAddWritesLockfileEntry(t *testing.T) {
	newProxy(t)

	projectDir := t.TempDir()
	lockFile, err := lock.GetFile(&testProject{projectDir: projectDir})
	require.NoError(t, err)
	require.NoError(t, lockFile.Add("go_1_22@1.22.1"))

	data, err := os.ReadFile(filepath.Join(projectDir, "devbox.lock"))
	require.NoError(t, err)
	var written struct {
		Packages map[string]struct {
			Resolved     string `json:"resolved"`
			Version      string `json:"version"`
			LastModified string `json:"last_modified"`
		} `json:"packages"`
	}
	require.NoError(t, json.Unmarshal(data, &written))

	entry := written.Packages["go_1_22@1.22.1"]
	assert.Equal(t,
		"github:NixOS/nixpkgs/58ae79ea707579c40102ddf62d84b902a987c58b#go_1_22",
		entry.Resolved)
	assert.Equal(t, "1.22.1", entry.Version)
	assert.Equal(t, "2024-03-06T17:57:40Z", entry.LastModified)
}

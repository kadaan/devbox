// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package searchproxy_test

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jetify.com/devbox/internal/boxcli"
	"go.jetify.com/devbox/internal/devbox"
	"go.jetify.com/devbox/internal/devbox/devopt"
	"go.jetify.com/devbox/internal/searcher"
)

func TestLegacyResolveExactPin(t *testing.T) {
	newProxy(t)

	got, err := searcher.Client().Resolve("go_1_22", "1.22.1")
	require.NoError(t, err)

	assert.Equal(t, "go_1_22", got.Name)
	assert.Equal(t, "1.22.1", got.Version)
	assert.Equal(t, "58ae79ea707579c40102ddf62d84b902a987c58b", got.CommitHash)
	assert.Equal(t, []string{"go_1_22"}, got.AttrPaths)

	require.Len(t, got.Systems, 4)
	for system, info := range got.Systems {
		assert.Equal(t, system, info.System)
		assert.Equal(t, "58ae79ea707579c40102ddf62d84b902a987c58b", info.CommitHash, system)
		assert.Equal(t, []string{"go_1_22"}, info.AttrPaths, system)
	}
}

func TestLegacyResolvePrintsSearchResolvesTo(t *testing.T) {
	newProxy(t)

	out := runDevbox(t, "search", "go_1_22@1.22.1")

	assert.Contains(t, out, "resolves to: go_1_22@1.22.1")
}

func TestLegacyResolveAgreesWithV2(t *testing.T) {
	newProxy(t)

	for _, pinned := range corpus(t) {
		key := pinned.Name + "@" + pinned.Constraint
		if _, isExpected := expectedFailures[key]; isExpected {
			continue
		}
		t.Run(key, func(t *testing.T) {
			legacy, err := searcher.Client().Resolve(pinned.Name, pinned.Constraint)
			require.NoError(t, err)
			modern, err := searcher.Client().ResolveV2(t.Context(), pinned.Name, pinned.Constraint)
			require.NoError(t, err)

			assert.Equal(t, modern.Version, legacy.Version)
			for system, info := range modern.Systems {
				assert.Equal(t, info.FlakeInstallable.Ref.Rev, legacy.CommitHash, system)
				assert.Equal(t, []string{info.FlakeInstallable.AttrPath}, legacy.AttrPaths, system)
			}
		})
	}
}

func TestLegacyResolveMissingPinIsNotFound(t *testing.T) {
	proxyURL, _ := newProxy(t)

	status := getStatus(t, proxyURL+"/v1/resolve?name=python&version=3.99.0")
	assert.Equal(t, http.StatusNotFound, status)

	got, err := searcher.Client().Resolve("python", "3.99.0")
	assert.ErrorIs(t, err, searcher.ErrNotFound)
	assert.Nil(t, got)
}

func TestLegacyResolveRendersDevboxInfo(t *testing.T) {
	newProxy(t)

	info, err := emptyProject(t).Info(t.Context(), "go_1_22@1.22.1", false)
	require.NoError(t, err)
	assert.Contains(t, info, "go_1_22 1.22.1")
}

func TestLegacyResolveMissingPinFallsThroughInDevboxInfo(t *testing.T) {
	newProxy(t)

	info, err := emptyProject(t).Info(t.Context(), "python@3.99.0", false)
	assert.Empty(t, info)
	assert.ErrorIs(t, err, searcher.ErrNotFound)
	assert.Contains(t, err.Error(), `Package "python@3.99.0" not found`)
}

func emptyProject(t *testing.T) *devbox.Devbox {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "devbox.json"), []byte("{}"), 0o600))
	box, err := devbox.Open(&devopt.Opts{Dir: dir, Stderr: io.Discard})
	require.NoError(t, err)
	return box
}

func runDevbox(t *testing.T, args ...string) string {
	t.Helper()
	out := &bytes.Buffer{}
	cmd := boxcli.RootCmd()
	cmd.SetArgs(args)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	require.NoError(t, cmd.ExecuteContext(t.Context()))
	return out.String()
}

func getStatus(t *testing.T, url string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	return resp.StatusCode
}

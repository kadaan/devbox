// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package searchproxy_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jetify.com/devbox/internal/searcher"
)

func TestResolveLatestPicksTheVersionAtIndexHead(t *testing.T) {
	newProxy(t)

	resolved, err := searcher.Client().ResolveV2(t.Context(), "jq", "latest")
	require.NoError(t, err)

	assert.Equal(t, "jq", resolved.Name)
	assert.Equal(t, "1.8.2", resolved.Version)
}

func TestResolveLatestCorpusPins(t *testing.T) {
	newProxy(t)

	pins := 0
	for _, pinned := range corpus(t) {
		if pinned.Constraint != "latest" {
			continue
		}
		pins++
		t.Run(pinned.Name, func(t *testing.T) {
			got, err := searcher.Client().ResolveV2(t.Context(), pinned.Name, "latest")
			require.NoError(t, err)
			assert.Equal(t, pinned.Version, got.Version)
		})
	}
	assert.Equal(t, 3, pins)
}

func TestResolveLatestPrefersHeadOverTheGreatestVersion(t *testing.T) {
	newProxy(t)

	resolved, err := searcher.Client().ResolveV2(t.Context(), "go-bindata", "latest")
	require.NoError(t, err)

	assert.Equal(t, "4.0.2", resolved.Version)
}

func TestResolveLatestFallsBackToTheGreatestVersion(t *testing.T) {
	newProxy(t)

	resolved, err := searcher.Client().ResolveV2(t.Context(), "jdk8", "latest")
	require.NoError(t, err)

	assert.Equal(t, "8u462-b08", resolved.Version)
}

func TestResolveLatestSkipsAPrereleaseAtHead(t *testing.T) {
	newProxy(t)

	resolved, err := searcher.Client().ResolveV2(t.Context(), "zig", "latest")
	require.NoError(t, err)

	assert.Equal(t, "0.14.0", resolved.Version)
}

func TestResolveConstraintFailsWhenTheIndexHasNoHead(t *testing.T) {
	proxyURL := newProxyWithIndexHead(t, "")

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		proxyURL+"/v2/resolve?name=go-bindata&version=latest", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
}

func TestResolvePrefixCorpusPins(t *testing.T) {
	newProxy(t)

	pins := 0
	for _, pinned := range corpus(t) {
		if pinned.Constraint == "latest" || pinned.exact() {
			continue
		}
		pins++
		t.Run(pinned.Name+"@"+pinned.Constraint, func(t *testing.T) {
			got, err := searcher.Client().ResolveV2(t.Context(), pinned.Name, pinned.Constraint)
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(got.Version, pinned.Constraint+"."),
				"%s is not within %s", got.Version, pinned.Constraint)
		})
	}
	assert.Equal(t, 4, pins)
}

func TestResolvePrefixComparesComponentsNotCharacters(t *testing.T) {
	newProxy(t)

	within, err := searcher.Client().ResolveV2(t.Context(), "nodejs", "22")
	require.NoError(t, err)
	assert.Equal(t, "nodejs_22", within.Name)
	assert.Equal(t, "22.14.0", within.Version)

	// nodejs_22 holds no 22.1.x, though "22.1" is a character prefix of its rows.
	outside, err := searcher.Client().ResolveV2(t.Context(), "nodejs", "22.1")
	assert.ErrorIs(t, err, searcher.ErrNotFound)
	assert.Nil(t, outside)
}

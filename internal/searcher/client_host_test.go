// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package searcher

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.jetify.com/devbox/internal/build"
	"go.jetify.com/devbox/internal/envir"
)

// The search host is baked in at build time with
// -X go.jetify.com/devbox/internal/build.SearchHost, so a fork can ship a binary
// pointing at a self-hosted proxy. DEVBOX_SEARCH_HOST still has to win over it,
// or a developer could not redirect a released binary.
func TestClientPrefersTheEnvironmentOverTheBuiltInHost(t *testing.T) {
	t.Setenv(envir.DevboxSearchHost, "http://127.0.0.1:8099")

	assert.Equal(t, "http://127.0.0.1:8099", Client().host)
}

func TestClientFallsBackToTheBuiltInHost(t *testing.T) {
	t.Setenv(envir.DevboxSearchHost, "")

	assert.Equal(t, build.SearchHost, Client().host)
}

// A self-hosted search host has no nixhub.io page: the "more information" link
// would point at an index that did not serve the results just printed, and may
// not carry the versions in them at all.
func TestServesNixhubFollowsTheConfiguredHost(t *testing.T) {
	t.Setenv(envir.DevboxSearchHost, "http://127.0.0.1:8099")
	assert.False(t, ServesNixhub())

	t.Setenv(envir.DevboxSearchHost, "https://search.devbox.sh")
	assert.True(t, ServesNixhub())

	t.Setenv(envir.DevboxSearchHost, "https://search.devbox.sh/")
	assert.True(t, ServesNixhub(), "a trailing slash names the same host")
}

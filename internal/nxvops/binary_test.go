// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package nxvops_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jetify.com/devbox/internal/nxvops"
)

func TestResolveBinaryPrefersAnExplicitExecutable(t *testing.T) {
	got, err := nxvops.ResolveBinary(t.Context(), "/usr/bin/true", "nixpkgs#hello")

	require.NoError(t, err)
	assert.Equal(t, "/usr/bin/true", got,
		"an explicit binary must be used as given, without invoking nix")
}

// The default must build the indexer variant. nxv's `index` subcommand is behind
// a Cargo feature its default build omits, so the plain nxv package produces a
// binary that cannot refresh an index.
func TestDefaultInstallableIsTheIndexerVariant(t *testing.T) {
	assert.True(t, strings.HasSuffix(nxvops.DefaultInstallable, "#nxv-indexer"),
		"got %q", nxvops.DefaultInstallable)
}

// Pinned to a revision, not a tag or a branch: either could be moved to
// different code under the same name, which would silently change the indexer
// that maintains the index.
func TestDefaultInstallableIsPinnedToARevision(t *testing.T) {
	ref, _, found := strings.Cut(nxvops.DefaultInstallable, "#")
	require.True(t, found, "installable has no attribute path: %q", nxvops.DefaultInstallable)

	rev := ref[strings.LastIndex(ref, "/")+1:]
	assert.Len(t, rev, 40, "%q is not a full git revision", rev)
	assert.NotContains(t, rev, ".", "%q looks like a tag rather than a revision", rev)
}

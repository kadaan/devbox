// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package searchproxy_test

import (
	"maps"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jetify.com/devbox/internal/searcher"
	"go.jetify.com/devbox/internal/searchproxy"
)

func TestResolveExactPin(t *testing.T) {
	newProxy(t)

	resolved, err := searcher.Client().ResolveV2(t.Context(), "go_1_22", "1.22.1")
	require.NoError(t, err)

	assert.Equal(t, "go_1_22", resolved.Name)
	assert.Equal(t, "1.22.1", resolved.Version)
	assert.ElementsMatch(t,
		[]string{"aarch64-darwin", "aarch64-linux", "x86_64-darwin", "x86_64-linux"},
		slices.Collect(maps.Keys(resolved.Systems)))
	for system, info := range resolved.Systems {
		assert.Equal(t,
			"github:NixOS/nixpkgs/58ae79ea707579c40102ddf62d84b902a987c58b#go_1_22",
			info.FlakeInstallable.String(), system)
		assert.Equal(t, time.Date(2024, 3, 6, 17, 57, 40, 0, time.UTC), info.LastUpdated, system)
		assert.Empty(t, info.Outputs, system)
	}
}

func TestResolveAbsentPin(t *testing.T) {
	newProxy(t)

	resolved, err := searcher.Client().ResolveV2(t.Context(), "go_1_22", "1.0.0")
	assert.ErrorIs(t, err, searcher.ErrNotFound)
	assert.Nil(t, resolved)
}

func TestResolveRejectsMissingVersion(t *testing.T) {
	proxyURL, _ := newProxy(t)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, proxyURL+"/v2/resolve?name=go_1_22", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// equivalentAttrPins are corpus pins whose exact version was also built by the
// unversioned attr, which AttrCandidates tries before the family suffixes.
var equivalentAttrPins = map[string]string{
	"go@1.22.1":      "go",
	"nodejs@24.12.0": "nodejs",
}

// expectedFailures are corpus pins the index cannot answer, and why.
var expectedFailures = map[string]string{
	"jdk@25.0.0": "nixpkgs' jdk25 calls this build 25+36; 25.0.0 exists only " +
		"under javaPackages.compiler.temurin-bin.jdk-25",
	"python@3.9.9": "no indexed channel carries python3 or python39 across the " +
		"3.9.5-3.9.12 era: python39 jumps 3.9.4 (2021-04) to 3.9.13 (2022-06). " +
		"Ingesting nixos-21.11 over 2021-11-29..2021-12-19, exactly when 3.9.9 " +
		"was current, added 88 rows but no python3 or python39 row at all",
}

func TestResolveCorpusExactPins(t *testing.T) {
	newProxy(t)

	pins := corpus(t)
	require.Len(t, pins, 146)

	exact, resolved, identicalAttr := 0, 0, 0
	for _, pinned := range pins {
		if !pinned.exact() {
			continue
		}
		exact++
		key := pinned.Name + "@" + pinned.Constraint
		t.Run(key, func(t *testing.T) {
			got, err := searcher.Client().ResolveV2(t.Context(), pinned.Name, pinned.Constraint)
			if cause, isExpected := expectedFailures[key]; isExpected {
				assert.ErrorIs(t, err, searcher.ErrNotFound, cause)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			resolved++
			assert.Equal(t, pinned.Version, got.Version)

			if unversioned, isEquivalent := equivalentAttrPins[key]; isEquivalent {
				assert.Contains(t, []string{pinned.Attr, unversioned}, got.Name)
				return
			}
			identicalAttr++
			assert.Equal(t, pinned.Attr, got.Name)
			require.NotEmpty(t, got.Systems)
			for system, info := range got.Systems {
				assert.Equal(t,
					"github:NixOS/nixpkgs/"+pinned.Rev+"#"+pinned.Attr,
					info.FlakeInstallable.String(), system)
			}
		})
	}

	assert.Equal(t, 139, exact)
	assert.Equal(t, 137, resolved)
	assert.Equal(t, 135, identicalAttr)
}

func TestResolveNodejsSuffixEras(t *testing.T) {
	newProxy(t)

	for _, era := range []struct{ version, attr string }{
		{"18.2.0", "nodejs-18_x"},
		{"18.19.1", "nodejs_18"},
	} {
		t.Run(era.attr, func(t *testing.T) {
			got, err := searcher.Client().ResolveV2(t.Context(), "nodejs", era.version)
			require.NoError(t, err)
			assert.Equal(t, era.attr, got.Name)
			assert.Equal(t, era.version, got.Version)
		})
	}
}

func TestResolveNeverLooksUpAPname(t *testing.T) {
	_, index := newProxy(t)

	_, err := searcher.Client().ResolveV2(t.Context(), "jdk", "25.0.0")
	require.ErrorIs(t, err, searcher.ErrNotFound)

	paths := index.requestedPaths()
	require.NotEmpty(t, paths)
	for _, path := range paths {
		// An exact constraint is looked up by attribute path only: the exact
		// row, then the per-system observations for that same attribute path.
		// Never the search endpoint, which matches on pname and description.
		assert.Regexp(t,
			`^/api/v1/packages/[^/]+/(versions/[^/]+|systems\?[^/]*)$`,
			path)
	}
}

func TestResolveDerivesFamilyAttr(t *testing.T) {
	newProxy(t)

	resolved, err := searcher.Client().ResolveV2(t.Context(), "python", "3.12.11")
	require.NoError(t, err)

	assert.Equal(t, "python312", resolved.Name)
	assert.Equal(t, "3.12.11", resolved.Version)
	for system, info := range resolved.Systems {
		assert.Equal(t,
			"github:NixOS/nixpkgs/a421ac6595024edcfbb1ef950a3712b89161c359#python312",
			info.FlakeInstallable.String(), system)
	}
}

func TestResolveSkipsCandidateWithoutTheVersion(t *testing.T) {
	newProxy(t)

	unversioned, err := searcher.Client().ResolveV2(t.Context(), "nodejs", "24.12.0")
	require.NoError(t, err)
	require.Equal(t, "nodejs", unversioned.Name)

	got, err := searcher.Client().ResolveV2(t.Context(), "nodejs", "22.10.0")
	require.NoError(t, err)
	assert.Equal(t, "nodejs_22", got.Name)
	assert.Equal(t, "22.10.0", got.Version)
}

func TestResolveWithNoVerifyingCandidate(t *testing.T) {
	newProxy(t)

	got, err := searcher.Client().ResolveV2(t.Context(), "python", "3.99.0")
	assert.ErrorIs(t, err, searcher.ErrNotFound)
	assert.Nil(t, got)
}

// TestResolveLatestReachesAFamilyAttr covers `devbox add python`: "latest"
// carries no version to derive a family suffix from, so the only candidate
// AttrCandidates yields is the bare name, which nixpkgs does not define.
func TestResolveLatestReachesAFamilyAttr(t *testing.T) {
	newProxy(t)

	got, err := searcher.Client().ResolveV2(t.Context(), "python", "latest")

	require.NoError(t, err)
	assert.Equal(t, "python3", got.Name)
	assert.Equal(t, "3.14.7", got.Version)
}

// nixpkgs names a family's current member with the least specific suffix, so
// python3 wins over python315 even though 3.15.0rc1 sorts higher. Selecting by
// version alone would install a release candidate.
func TestResolveLatestPrefersTheLeastSpecificFamilyAttr(t *testing.T) {
	newProxy(t)

	got, err := searcher.Client().ResolveV2(t.Context(), "python", "latest")

	require.NoError(t, err)
	assert.NotEqual(t, "python315", got.Name)
	assert.NotContains(t, got.Version, "rc")
}

// Only attribute paths that are mechanical family members are accepted, so a
// search hit matching on pname or description never resolves. python-launcher,
// python3Minimal and python314FreeThreading are all in this query's results.
func TestResolveLatestRejectsNonFamilySearchHits(t *testing.T) {
	newProxy(t)

	for _, attr := range []string{"python-launcher", "python3Minimal", "python314FreeThreading"} {
		assert.False(t, searchproxy.IsFamilyAttr("python", attr), attr)
	}
	assert.True(t, searchproxy.IsFamilyAttr("python", "python3"))
	assert.True(t, searchproxy.IsFamilyAttr("python", "python314"))
}

// A name with no family members at all stays a 404 rather than resolving to
// something the search merely mentioned.
func TestResolveLatestStillFailsWithNoFamilyMember(t *testing.T) {
	newProxy(t)

	got, err := searcher.Client().ResolveV2(t.Context(), "nosuchpackage", "latest")

	assert.ErrorIs(t, err, searcher.ErrNotFound)
	assert.Nil(t, got)
}

// The attribute path "python" exists but is the retired Python 2 line, whose
// newest row is 2.7.18 from 2020. "latest" must not answer with it.
func TestResolveLatestSkipsARetiredAttr(t *testing.T) {
	newProxy(t)

	got, err := searcher.Client().ResolveV2(t.Context(), "python", "latest")

	require.NoError(t, err)
	assert.Equal(t, "python3", got.Name)
	assert.NotEqual(t, "2.7.18", got.Version)
}

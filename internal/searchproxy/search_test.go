// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package searchproxy_test

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jetify.com/devbox/internal/nxv"
	"go.jetify.com/devbox/internal/searcher"
	"go.jetify.com/devbox/internal/searchproxy"
)

func TestSearchParsesThroughDevboxClient(t *testing.T) {
	newProxy(t)

	results, err := searcher.Client().Search(t.Context(), "python")
	require.NoError(t, err)

	names := make([]string, len(results.Packages))
	for i, pkg := range results.Packages {
		names[i] = pkg.Name
	}
	assert.Equal(t, searchAttrs(t, "python"), names)
	assert.Equal(t, len(names), results.NumResults)

	for _, pkg := range results.Packages {
		assert.Equal(t, len(pkg.Versions), pkg.NumVersions, pkg.Name)
		require.NotEmpty(t, pkg.Versions, pkg.Name)
		for _, version := range pkg.Versions {
			assert.NotEmpty(t, version.Version, pkg.Name)
			systems := slices.Collect(maps.Keys(version.Systems))
			assert.NotEmpty(t, systems, pkg.Name)
			// A package reports the systems nixpkgs lists for it, which is a
			// subset of Devbox's: recent python carries no x86_64-darwin.
			assert.Subset(t,
				[]string{"aarch64-darwin", "aarch64-linux", "x86_64-darwin", "x86_64-linux"},
				systems, pkg.Name)
		}
	}
}

func TestSearchNamesAreAttributePaths(t *testing.T) {
	proxyURL, _ := newProxy(t)

	results := getJSON[searcher.SearchResultsV2](t, proxyURL+"/v2/search?q=python")
	require.Equal(t, len(searchAttrs(t, "python")), results.TotalResults)
	require.NotEmpty(t, results.Results)

	for _, result := range results.Results {
		pkg := getJSON[searcher.PackageV2](t, proxyURL+"/v2/pkg?name="+url.QueryEscape(result.Name))
		assert.Equal(t, result.Name, pkg.Name)
		assert.NotEmpty(t, pkg.Releases, result.Name)
	}
}

func TestPackageReturnsEveryRelease(t *testing.T) {
	proxyURL, _ := newProxy(t)

	rows := loadJSON[map[string]struct {
		Data []nxv.PackageVersion `json:"data"`
	}](t, "nxv", "packages.json")["python312"].Data
	require.NotEmpty(t, rows)

	byVersion := make(map[string]nxv.PackageVersion, len(rows))
	for _, row := range rows {
		byVersion[row.Version] = row
	}

	pkg := getJSON[searcher.PackageV2](t, proxyURL+"/v2/pkg?name=python312")
	require.Len(t, pkg.Releases, len(rows))
	for _, release := range pkg.Releases {
		row, held := byVersion[release.Version]
		require.True(t, held, release.Version)
		delete(byVersion, release.Version)
		assert.Equal(t, row.FirstCommitDate, release.LastUpdated)
		require.Len(t, release.Platforms, len(row.Platforms))
		for _, platform := range release.Platforms {
			assert.Equal(t, "python312", platform.AttributePath)
			assert.Equal(t, row.FirstCommitHash, platform.CommitHash)
			assert.Equal(t, row.FirstCommitDate, platform.Date)
		}
	}
	assert.Empty(t, byVersion)
}

func TestPackageOrdersReleasesNewestFirst(t *testing.T) {
	proxyURL, _ := newProxy(t)

	pkg := getJSON[searcher.PackageV2](t, proxyURL+"/v2/pkg?name=python312")

	require.Greater(t, len(pkg.Releases), 1)
	for i, release := range pkg.Releases[1:] {
		assert.False(t, release.LastUpdated.After(pkg.Releases[i].LastUpdated),
			"release %s is newer than the one before it", release.Version)
	}
}

// searchAttrs returns the distinct attribute paths the index fixture answers
// query with, sorted the way the proxy returns them, so a test asserts against
// the recording rather than a copy of it. The index returns a row per
// (attribute path, version), so a path repeats across its versions.
func searchAttrs(t *testing.T, query string) []string {
	t.Helper()
	hits := loadJSON[map[string]struct {
		Data []nxv.PackageVersion `json:"data"`
	}](t, "nxv", "search.json")[query].Data

	attrs := make([]string, 0, len(hits))
	for _, hit := range hits {
		if !slices.Contains(attrs, hit.AttributePath) {
			attrs = append(attrs, hit.AttributePath)
		}
	}
	slices.Sort(attrs)
	return attrs
}

// The index ranks an exact match last -- searching "jq" returns jquake,
// jq-zsh-plugin, jqp, jqfmt, then jq -- so results are ordered by attribute
// path instead. A name is a prefix of its longer variants, which puts the exact
// match at the top of its family.
func TestSearchOrdersResultsByAttributePath(t *testing.T) {
	proxyURL, _ := newProxy(t)

	results := getJSON[searcher.SearchResultsV2](t, proxyURL+"/v2/search?q=python")

	names := make([]string, len(results.Results))
	for i, result := range results.Results {
		names[i] = result.Name
	}
	require.NotEmpty(t, names)
	assert.True(t, slices.IsSorted(names), "got %v", names)

	// The point of the ordering: a name precedes its longer variants.
	assert.Less(t, slices.Index(names, "python3"), slices.Index(names, "python310"))
	assert.Less(t, slices.Index(names, "python314"), slices.Index(names, "python314FreeThreading"))
}

// The index returns a row per (attribute path, version), so a package with
// several indexed versions comes back several times. Devbox renders one table
// row per result, so passing them through repeats the package in `devbox
// search`.
func TestSearchReportsEachPackageOnce(t *testing.T) {
	proxyURL, index := newProxy(t)

	rows := loadJSON[map[string]struct {
		Data []nxv.PackageVersion `json:"data"`
	}](t, "nxv", "search.json")["python"].Data
	distinct := searchAttrs(t, "python")
	require.Less(t, len(distinct), len(rows),
		"the fixture must contain a repeated attribute path to exercise this")

	results := getJSON[searcher.SearchResultsV2](t, proxyURL+"/v2/search?q=python")

	names := make([]string, len(results.Results))
	for i, result := range results.Results {
		names[i] = result.Name
	}
	assert.Equal(t, distinct, names)
	assert.Equal(t, len(distinct), results.TotalResults,
		"the count must match what is returned")

	// Devbox fetches /v2/pkg per result, so a duplicate costs a request too.
	pkgRequests := 0
	for _, path := range index.requestedPaths() {
		if strings.Contains(path, "/api/v1/packages/") {
			pkgRequests++
		}
	}
	assert.Zero(t, pkgRequests, "search alone should not fetch packages")
}

// A repeated path still carries the freshest timestamp of its versions.
func TestSearchTakesTheNewestTimestampAcrossVersions(t *testing.T) {
	proxyURL, _ := newProxy(t)

	rows := loadJSON[map[string]struct {
		Data []nxv.PackageVersion `json:"data"`
	}](t, "nxv", "search.json")["python"].Data

	newest := map[string]time.Time{}
	for _, row := range rows {
		if row.LastCommitDate.After(newest[row.AttributePath]) {
			newest[row.AttributePath] = row.LastCommitDate
		}
	}

	results := getJSON[searcher.SearchResultsV2](t, proxyURL+"/v2/search?q=python")
	require.NotEmpty(t, results.Results)
	for _, result := range results.Results {
		assert.Equal(t, newest[result.Name], result.LastUpdated, result.Name)
	}
}

func TestSearchNormalizesPlatformsToNixSystems(t *testing.T) {
	newProxy(t)

	results, err := searcher.Client().Search(t.Context(), "platform-legacy")
	require.NoError(t, err)
	require.Len(t, results.Packages, 1)
	require.Len(t, results.Packages[0].Versions, 1)

	assert.ElementsMatch(t,
		[]string{"aarch64-darwin", "x86_64-linux"},
		slices.Collect(maps.Keys(results.Packages[0].Versions[0].Systems)))
}

func getJSON[T any](t *testing.T, url string) T {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, url)

	var value T
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&value))
	return value
}

// nixpkgs lists dozens of platforms per package, down to m68k-linux and
// i686-cygwin. Reporting them all fills devbox search's platform column with
// targets Devbox cannot build for.
func TestPackageReportsOnlyTheConfiguredSystems(t *testing.T) {
	index := newFakeIndex(t)
	proxyURL := startProxyWithSystems(t, index, []string{"aarch64-darwin", "x86_64-linux"})

	pkg := getJSON[searcher.PackageV2](t, proxyURL+"/v2/pkg?name=wide-platforms")

	require.NotEmpty(t, pkg.Releases)
	for _, release := range pkg.Releases {
		for _, platform := range release.Platforms {
			assert.Contains(t,
				[]string{"aarch64-darwin", "x86_64-linux"},
				platform.Arch+"-"+platform.OS, release.Version)
		}
	}
}

// An empty system list reports whatever nixpkgs lists, for a caller that wants
// the unfiltered view. The wide-platforms fixture is a recorded row carrying
// nixpkgs' full list, which runs to 31 systems.
func TestPackageReportsEverySystemWhenUnfiltered(t *testing.T) {
	index := newFakeIndex(t)
	filtered := startProxyWithSystems(t, index, searchproxy.DefaultSystems)
	unfiltered := startProxyWithSystems(t, index, nil)

	count := func(url string) int {
		pkg := getJSON[searcher.PackageV2](t, url+"/v2/pkg?name=wide-platforms")
		require.NotEmpty(t, pkg.Releases)
		return len(pkg.Releases[0].Platforms)
	}

	assert.Greater(t, count(unfiltered), 20, "the fixture carries nixpkgs' full list")
	assert.LessOrEqual(t, count(filtered), len(searchproxy.DefaultSystems))
}

// The filter has to agree with the normalization Devbox's client applies, or a
// requested system would be dropped under a different spelling.
func TestSystemFilterMatchesDevboxNormalization(t *testing.T) {
	index := newFakeIndex(t)
	proxyURL := startProxyWithSystems(t, index, []string{"aarch64-darwin"})

	pkg := getJSON[searcher.PackageV2](t, proxyURL+"/v2/pkg?name=platform-legacy")

	require.NotEmpty(t, pkg.Releases)
	require.Len(t, pkg.Releases[0].Platforms, 1,
		"arm64-macOS must match the requested aarch64-darwin")
	assert.Equal(t, "arm64", pkg.Releases[0].Platforms[0].Arch)
}

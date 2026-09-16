// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package searchproxy_test

import (
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
	"go.jetify.com/devbox/nix"
)

// The divergence every test here is about, in the numbers nixpkgs and nixhub
// actually carry for it: at commit 01b6809f, jdk25 is openjdk 25+36 on Linux
// and zulu 25.0.0 on Darwin. The index row is the snapshot system's — 25+36 —
// and it is the only version any row records.
const (
	// otherVersion is what the snapshot system saw, and what the index row in
	// testdata carries.
	otherVersion = "25+36"
	// localVersion is what the resolving machine's own system saw. No index
	// row holds it.
	localVersion = "25.0.0"
	jdk25Rev     = "01b6809f7f9d1183a2b3e081f0a1e6f8f415cb09"
)

// jdk25Observations answers the systems endpoint the way a per-system index
// does, giving the host's own system a version no index row carries.
//
// Keyed off [nix.System] rather than a hardcoded system so the tests assert the
// resolving machine's answer wherever they run.
func jdk25Observations(attr string, query url.Values) []nxv.SystemObservation {
	if attr != "jdk25" {
		return nil
	}
	observed := time.Date(2025, 10, 22, 20, 59, 19, 0, time.UTC)

	var rows []nxv.SystemObservation
	for _, system := range []string{
		"aarch64-darwin", "aarch64-linux", "x86_64-darwin", "x86_64-linux",
	} {
		version, storeName := otherVersion, "openjdk"
		if system == nix.System() {
			version, storeName = localVersion, "zulu-ca-jdk"
		}
		// x86_64-darwin is gone from nixpkgs: the index records that as an
		// observation with no version, and it must never be reported as a
		// system a package resolves on.
		if system == "x86_64-darwin" && system != nix.System() {
			version, storeName = "", ""
		}

		row := nxv.SystemObservation{
			AttributePath: attr,
			System:        system,
			CommitHash:    jdk25Rev,
			CommitDate:    observed,
			Version:       version,
			StoreName:     storeName,
			ObservedAt:    observed,
		}
		if version != "" {
			row.StorePath = "/nix/store/aaaa-" + storeName + "-" + version
			row.Outputs = []nxv.Output{
				{Name: "out", Path: row.StorePath, Default: true},
				{Name: "debug", Path: "/nix/store/bbbb-" + storeName + "-debug"},
			}
		}
		rows = append(rows, row)
	}

	if wanted := query.Get("system"); wanted != "" {
		systems := strings.Split(wanted, ",")
		rows = slices.DeleteFunc(rows, func(row nxv.SystemObservation) bool {
			return !slices.Contains(systems, row.System)
		})
	}
	if wanted := query.Get("version"); wanted != "" {
		rows = slices.DeleteFunc(rows, func(row nxv.SystemObservation) bool {
			return row.Version != wanted
		})
	}
	return rows
}

func newPerSystemProxy(t *testing.T) *fakeIndex {
	t.Helper()
	index := newFakeIndex(t)
	index.systems = jdk25Observations
	startProxy(t, index)
	return index
}

func TestResolveReportsTheVersionThisSystemActuallyGets(t *testing.T) {
	newPerSystemProxy(t)

	resolved, err := searcher.Client().ResolveV2(t.Context(), "jdk25", otherVersion)
	require.NoError(t, err)

	assert.Equal(t, localVersion, resolved.Version,
		"the resolving system's own version, not the snapshot system's")
	assert.Equal(t, []string{nix.System()}, systemsOf(resolved),
		"only the systems on which that version is what evaluates")
	assert.Equal(t,
		"github:NixOS/nixpkgs/"+jdk25Rev+"#jdk25",
		resolved.Systems[nix.System()].FlakeInstallable.String(),
		"the pin is unchanged: one commit, one attribute path")
}

func TestResolveCarriesPerSystemStorePaths(t *testing.T) {
	newPerSystemProxy(t)

	resolved, err := searcher.Client().ResolveV2(t.Context(), "jdk25", otherVersion)
	require.NoError(t, err)

	outputs := resolved.Systems[nix.System()].Outputs
	require.Len(t, outputs, 2, "both outputs, so the binary cache can be used")
	assert.Equal(t, "out", outputs[0].Name)
	assert.True(t, outputs[0].Default)
	assert.Equal(t, "/nix/store/aaaa-zulu-ca-jdk-"+localVersion, outputs[0].Path,
		"the store path of the derivation this system builds")
	assert.Equal(t, "debug", outputs[1].Name)
	assert.False(t, outputs[1].Default)
}

func TestResolveFindsAVersionOnlyThisSystemEverSaw(t *testing.T) {
	index := newPerSystemProxy(t)

	// No index row carries 25.0.3 for jdk25 — every snapshot at those commits
	// saw an openjdk version — so this can only resolve through an observation.
	resolved, err := searcher.Client().ResolveV2(t.Context(), "jdk25", localVersion)
	require.NoError(t, err)

	assert.Equal(t, "jdk25", resolved.Name)
	assert.Equal(t, localVersion, resolved.Version)
	assert.Equal(t, []string{nix.System()}, systemsOf(resolved))
	assert.Equal(t,
		"github:NixOS/nixpkgs/"+jdk25Rev+"#jdk25",
		resolved.Systems[nix.System()].FlakeInstallable.String(),
		"pinned at the commit the observation was made at")

	assert.Contains(t, index.requestedPaths(),
		"/api/v1/packages/jdk25/versions/"+url.PathEscape(localVersion),
		"the index row is still tried first")
}

func TestResolveWithoutPerSystemObservationsKeepsAnsweringFromTheRow(t *testing.T) {
	// An index that 404s the systems endpoint is every published index today.
	newProxy(t)

	resolved, err := searcher.Client().ResolveV2(t.Context(), "jdk25", otherVersion)
	require.NoError(t, err)

	assert.Equal(t, otherVersion, resolved.Version)
	assert.Equal(t, searchproxyDefaultSystems(), systemsOf(resolved),
		"every configured system, as before per-system observations existed")
	for system, info := range resolved.Systems {
		assert.Empty(t, info.Outputs, system)
	}
}

func TestResolvePrefixConstraintNeverSearchesForAPerSystemVersion(t *testing.T) {
	index := newPerSystemProxy(t)

	// "25" selects a row; it is not a version any system is at. Looking for it
	// per system would evaluate every commit of the attribute path to find
	// nothing.
	resolved, err := searcher.Client().ResolveV2(t.Context(), "jdk25", "25")
	require.NoError(t, err)
	assert.Equal(t, localVersion, resolved.Version)

	for _, path := range index.requestedPaths() {
		assert.NotContains(t, path, "version=25&",
			"a prefix must not be handed to the per-system version search")
		assert.NotContains(t, path, "systems?system=aarch64-darwin&version=25",
			"a prefix must not be handed to the per-system version search")
	}
}

func TestResolveTreatsAnEmptyObservationSetAsNotObserved(t *testing.T) {
	index := newFakeIndex(t)
	// An index with the endpoint but nothing recorded, and no evaluation to
	// record anything with.
	index.systems = func(string, url.Values) []nxv.SystemObservation { return nil }
	startProxy(t, index)

	resolved, err := searcher.Client().ResolveV2(t.Context(), "jdk25", otherVersion)
	require.NoError(t, err)

	assert.Equal(t, otherVersion, resolved.Version)
	assert.Equal(t, searchproxyDefaultSystems(), systemsOf(resolved),
		"nothing observed is not the same as resolving nowhere")
}

func TestResolveDropsASystemTheAttributePathDoesNotResolveOn(t *testing.T) {
	newPerSystemProxy(t)

	resolved, err := searcher.Client().ResolveV2(t.Context(), "jdk25", otherVersion)
	require.NoError(t, err)

	assert.NotContains(t, systemsOf(resolved), "x86_64-darwin",
		"nixpkgs dropped the platform; reporting it would pin something unbuildable")
}

// searchproxyDefaultSystems is the sorted set the proxy reports when it has no
// per-system answer.
func searchproxyDefaultSystems() []string {
	systems := slices.Clone(searchproxy.DefaultSystems)
	slices.Sort(systems)
	return systems
}

func systemsOf(resolved *searcher.ResolveResponse) []string {
	systems := make([]string, 0, len(resolved.Systems))
	for system := range resolved.Systems {
		systems = append(systems, system)
	}
	slices.Sort(systems)
	return systems
}

// The failure this covers: an index row spans the commits one system saw a
// version over, and platform support moves inside that span. dumb-init 1.2.5
// runs from 2021 to today; aarch64-darwin gained it in the middle, so the row's
// first commit has it on Linux and does not have it on Darwin at all.
func TestResolvePinsTheCommitWhereEachSystemHasTheVersion(t *testing.T) {
	const (
		rowCommit    = "30ab92ea31f6b7e9095b1e7e4b56a5000823efdf"
		darwinCommit = "074b06a122e9f3e245e17fdc1199c3481c8b70b5"
	)
	observed := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)

	index := newFakeIndex(t)
	index.systems = func(attr string, query url.Values) []nxv.SystemObservation {
		if attr != "dumb-init" {
			return nil
		}
		row := func(system, commit, storePath string) nxv.SystemObservation {
			seen := nxv.SystemObservation{
				AttributePath: attr,
				System:        system,
				CommitHash:    commit,
				CommitDate:    observed,
				Version:       "1.2.5",
				StoreName:     "dumb-init",
				StorePath:     storePath,
				ObservedAt:    observed,
			}
			if storePath != "" {
				seen.Outputs = []nxv.Output{{Name: "out", Path: storePath, Default: true}}
			}
			return seen
		}

		wanted := strings.Split(query.Get("system"), ",")
		var rows []nxv.SystemObservation
		for _, system := range wanted {
			switch {
			case system != nix.System():
				rows = append(rows, row(system, rowCommit, "/nix/store/aaaa-dumb-init-1.2.5"))
			case query.Get("version") != "":
				// Asked where this system has it: the later commit, where it does.
				rows = append(rows, row(system, darwinCommit, "/nix/store/bbbb-dumb-init-1.2.5"))
			default:
				// At the row's commit it evaluates but has no output path.
				rows = append(rows, row(system, rowCommit, ""))
			}
		}
		return rows
	}
	startProxy(t, index)

	resolved, err := searcher.Client().ResolveV2(t.Context(), "dumb-init", "1.2.5")
	require.NoError(t, err)

	assert.Equal(t, "1.2.5", resolved.Version)
	local := resolved.Systems[nix.System()]
	assert.Equal(t,
		"github:NixOS/nixpkgs/"+darwinCommit+"#dumb-init",
		local.FlakeInstallable.String(),
		"this system must be pinned where it has the version, not where the row starts")
	require.Len(t, local.Outputs, 1)
	assert.Equal(t, "/nix/store/bbbb-dumb-init-1.2.5", local.Outputs[0].Path)

	for system, info := range resolved.Systems {
		if system == nix.System() {
			continue
		}
		assert.Equal(t,
			"github:NixOS/nixpkgs/"+rowCommit+"#dumb-init",
			info.FlakeInstallable.String(),
			"%s has it at the row's commit and keeps it", system)
	}
}

// A version with no output path cannot be installed, whatever it claims.
func TestResolveDropsASystemWithNoStorePathAnywhere(t *testing.T) {
	index := newFakeIndex(t)
	index.systems = func(attr string, query url.Values) []nxv.SystemObservation {
		if attr != "dumb-init" {
			return nil
		}
		var rows []nxv.SystemObservation
		for _, system := range strings.Split(query.Get("system"), ",") {
			rows = append(rows, nxv.SystemObservation{
				AttributePath: attr,
				System:        system,
				CommitHash:    "30ab92ea31f6b7e9095b1e7e4b56a5000823efdf",
				Version:       "1.2.5",
				StoreName:     "dumb-init",
			})
		}
		return rows
	}
	startProxy(t, index)

	resolved, err := searcher.Client().ResolveV2(t.Context(), "dumb-init", "1.2.5")
	require.NoError(t, err)
	assert.Equal(t, []string{nix.System()}, systemsOf(resolved),
		"a response may not be empty, but nothing else may claim to install it")
	assert.Empty(t, resolved.Systems[nix.System()].Outputs)
}

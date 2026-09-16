// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package searchproxy

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"go.jetify.com/devbox/internal/nxv"
	"go.jetify.com/devbox/internal/searcher"
)

func searchV2(w http.ResponseWriter, r *http.Request, index *nxv.Client) {
	query := r.URL.Query().Get("q")
	if query == "" {
		writeError(w, http.StatusBadRequest, "q is required")
		return
	}

	rows, err := index.Search(r.Context(), query)
	if err != nil && !errors.Is(err, nxv.ErrNotFound) {
		slog.Error("search", "q", query, "err", err)
		writeError(w, http.StatusBadGateway, "index lookup failed")
		return
	}

	writeJSON(w, http.StatusOK, searchResults(query, rows))
}

// searchResults collapses the index's rows to one result per attribute path,
// ordered by that path.
//
// nxv's search returns a row per (attribute path, version), so a package with
// several indexed versions comes back several times. Devbox renders one table
// row per result and fetches /v2/pkg for each, so passing them through repeats
// the same package -- and repeats the request that describes it.
//
// Sorting replaces the index's own ordering, which ranks an exact match last:
// searching "jq" returns jquake, jq-zsh-plugin, jqp, jqfmt, then jq. A name is a
// prefix of its longer variants, so ordering by path puts the exact match at the
// top of its family and makes a long --show-all listing scannable.
func searchResults(query string, rows []nxv.PackageVersion) searcher.SearchResultsV2 {
	results := searcher.SearchResultsV2{Query: query}
	position := make(map[string]int, len(rows))

	for _, row := range rows {
		index, seen := position[row.AttributePath]
		if !seen {
			position[row.AttributePath] = len(results.Results)
			results.Results = append(results.Results, searcher.SearchResultV2{
				Name:        row.AttributePath,
				Summary:     row.Description,
				LastUpdated: row.LastCommitDate,
			})
			continue
		}
		// Later rows for the path are other versions of it, so they still carry
		// the freshest timestamp and a description the first row may lack.
		existing := &results.Results[index]
		if existing.Summary == "" {
			existing.Summary = row.Description
		}
		if row.LastCommitDate.After(existing.LastUpdated) {
			existing.LastUpdated = row.LastCommitDate
		}
	}

	slices.SortFunc(results.Results, func(a, b searcher.SearchResultV2) int {
		return strings.Compare(a.Name, b.Name)
	})
	results.TotalResults = len(results.Results)
	return results
}

func pkgV2(w http.ResponseWriter, r *http.Request, index *nxv.Client, systems []string) {
	// name is already an attribute path, so it needs no candidate expansion.
	attr := r.URL.Query().Get("name")
	if attr == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	rows, err := index.Versions(r.Context(), attr)
	switch {
	case errors.Is(err, nxv.ErrNotFound):
		writeError(w, http.StatusNotFound, attr+" is not in the index")
	case err != nil:
		slog.Error("pkg", "name", attr, "err", err)
		writeError(w, http.StatusBadGateway, "index lookup failed")
	default:
		writeJSON(w, http.StatusOK, packageResponse(attr, rows, systems))
	}
}

// packageResponse orders releases newest first: devbox search renders only the
// first ten.
func packageResponse(attr string, rows []nxv.PackageVersion, systems []string) searcher.PackageV2 {
	pkg := searcher.PackageV2{Name: attr, Releases: make([]searcher.Release, len(rows))}
	for i, row := range rows {
		if pkg.Summary == "" {
			pkg.Summary = row.Description
		}
		if pkg.HomepageURL == "" {
			pkg.HomepageURL = row.Homepage
		}
		pkg.Releases[i] = searcher.Release{
			Version:     row.Version,
			LastUpdated: row.FirstCommitDate,
			Platforms:   platforms(row, systems),
		}
	}
	slices.SortStableFunc(pkg.Releases, func(a, b searcher.Release) int {
		return CompareVersions(b.Version, a.Version)
	})
	return pkg
}

// platforms splits each nxv "arch-os" platform into a [searcher.Platform],
// dropping any that has no separator and any system not in systems.
//
// nixpkgs lists dozens of platforms per package, down to m68k-linux and
// i686-cygwin. Reporting them all fills `devbox search`'s platform column with
// targets Devbox cannot build for; an empty systems reports every one.
func platforms(row nxv.PackageVersion, systems []string) []searcher.Platform {
	result := make([]searcher.Platform, 0, len(row.Platforms))
	for _, platform := range row.Platforms {
		split := strings.LastIndex(platform, "-")
		if split <= 0 {
			slog.Error("pkg: dropping platform with no arch-os separator",
				"name", row.AttributePath, "version", row.Version, "platform", platform)
			continue
		}
		if len(systems) > 0 && !slices.Contains(systems, nixSystem(platform[:split], platform[split+1:])) {
			continue
		}
		result = append(result, searcher.Platform{
			Arch:          platform[:split],
			OS:            platform[split+1:],
			AttributePath: row.AttributePath,
			CommitHash:    row.FirstCommitHash,
			Date:          row.FirstCommitDate,
		})
	}
	return result
}

// nixSystem maps an nxv "arch-os" pair to a Nix system identifier, mirroring the
// normalization Devbox's search client applies when it reads the response
// (internal/searcher/client.go). Filtering has to agree with it, or a system the
// caller asked for would be dropped under a different spelling.
func nixSystem(arch, os string) string {
	arch = strings.ReplaceAll(arch, "-", "_")
	if arch == "arm64" {
		arch = "aarch64"
	}
	os = strings.ToLower(os)
	if os == "macos" {
		os = "darwin"
	}
	return arch + "-" + os
}

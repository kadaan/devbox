// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package searchproxy

import (
	"context"
	"net/http"

	"go.jetify.com/devbox/internal/nxv"
	"go.jetify.com/devbox/internal/searcher"
)

func resolveV1(w http.ResponseWriter, r *http.Request, index *nxv.Client, systems []string) {
	handleResolve(w, r, index, packageVersion(systems))
}

// packageVersion serializes the pre-v2 shape, which repeats one version under
// every system.
//
// Left on the row's version deliberately: Devbox reaches this only with
// DEVBOX_FEATURE_RESOLVE_V2=0, and the per-system answer needs the response to
// be able to omit a system — which this shape, whose caller reads
// `systems[nix.System()].Version` and errors when it is missing, cannot do
// without turning a resolvable package into a hard failure.
func packageVersion(systems []string) func(context.Context, *nxv.PackageVersion) searcher.PackageVersion {
	return func(_ context.Context, row *nxv.PackageVersion) searcher.PackageVersion {
		info := searcher.PackageInfo{
			CommitHash:  row.FirstCommitHash,
			LastUpdated: int(row.FirstCommitDate.Unix()),
			AttrPaths:   []string{row.AttributePath},
			Version:     row.Version,
			Summary:     row.Description,
		}

		resp := searcher.PackageVersion{
			PackageInfo: info,
			Name:        row.AttributePath,
			Systems:     make(map[string]searcher.PackageInfo, len(systems)),
		}
		// The legacy shape repeats the rev and attribute path under every system.
		for _, system := range systems {
			perSystem := info
			perSystem.System = system
			resp.Systems[system] = perSystem
		}
		return resp
	}
}

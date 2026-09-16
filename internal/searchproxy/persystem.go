// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package searchproxy

import (
	"context"
	"errors"
	"log/slog"

	"go.jetify.com/devbox/internal/nxv"
	"go.jetify.com/devbox/internal/searcher"
	"go.jetify.com/devbox/nix"
	"go.jetify.com/devbox/nix/flake"
)

// perSystem holds what each requested system evaluated an attribute path to at
// one commit, keyed by system.
//
// An index row's version is whichever system the channel snapshot described, so
// it is only accidentally true for the others. nixpkgs defines jdk25 as openjdk
// on Linux and zulu on Darwin, so a Darwin resolve that reports the row's
// version names a version that does not exist on the machine doing the
// resolving. These observations are what replace that guess.
type perSystem map[string]nxv.SystemObservation

// observe asks the index what each of systems evaluated attr to at commit.
//
// A nil result means the index cannot answer per-system, and the caller must
// fall back to the row. That is not the same as an empty answer, and the two
// have to stay distinguishable: an index that answers "no systems" is telling
// the caller the package resolves nowhere.
func observe(ctx context.Context, index *nxv.Client, attr, commit string, systems []string) perSystem {
	rows, err := index.SystemsAtCommit(ctx, attr, commit, systems)
	if err != nil {
		// Degrade to the row rather than failing the resolve: an index without
		// the systems endpoint, or one that cannot reach nix, can still answer
		// everything Devbox asked for before per-system observations existed.
		// Logged rather than swallowed, because the fallback answer carries a
		// version that is only right for the snapshot system.
		slog.Warn("resolve: falling back to the index row's version",
			"attr", attr, "commit", commit, "err", err)
		return nil
	}

	observed := make(perSystem, len(rows))
	for _, row := range rows {
		observed[row.System] = row
	}
	return observed
}

// version returns the version to report for system, and whether an observation
// carried it.
func (p perSystem) version(system string) (string, bool) {
	row, ok := p[system]
	if !ok || !row.Available() {
		return "", false
	}
	return row.Version, true
}

// resolveResponse builds the /v2/resolve answer for row, reporting each
// system's own version rather than the row's.
//
// The response has one version field and a set of systems, so it can only
// describe systems that agree: the version is the one the resolving machine
// will get, and the systems are those on which that same version can be
// installed. A system whose version differs is left out — a caller pinning this
// answer would otherwise record a version that system never had.
//
// Each system carries its own commit. An index row spans the commits over which
// the snapshot system saw a version, and platform support moves inside that
// span: dumb-init 1.2.5 runs from 2021 to today, and aarch64-darwin gained it
// somewhere in the middle, so the row's first commit builds on Linux and does
// not exist on Darwin. nixhub answers the same shape, naming one commit for
// aarch64-darwin and another for x86_64-darwin for that very package.
func resolveResponse(ctx context.Context, index *nxv.Client, row *nxv.PackageVersion,
	systems []string,
) searcher.ResolveResponse {
	installable := flake.Installable{
		Ref: flake.Ref{
			Type:  flake.TypeGitHub,
			Owner: "NixOS",
			Repo:  "nixpkgs",
			Rev:   row.FirstCommitHash,
		},
		AttrPath: row.AttributePath,
	}

	resp := searcher.ResolveResponse{
		Name:    row.AttributePath,
		Version: row.Version,
	}
	resp.Systems = makeMapLike(resp.Systems, len(systems))

	// An empty answer is "not observed", never "resolves nowhere": an index
	// that has looked reports a row per system, carrying no version for the
	// ones an attribute path does not resolve on. So an index that has nothing
	// recorded and will not evaluate falls back to the row, same as one
	// without the endpoint at all.
	observed := observe(ctx, index, row.AttributePath, row.FirstCommitHash, systems)
	if len(observed) == 0 {
		for _, system := range systems {
			info := resp.Systems[system]
			info.FlakeInstallable = installable
			info.LastUpdated = row.FirstCommitDate
			resp.Systems[system] = info
		}
		return resp
	}

	// The resolving machine's own answer is the one the response must be true
	// for. When the package does not resolve there at all, the row's version
	// keeps the response non-empty for the systems that do have it, which is
	// what lets a shared devbox.json resolve on a machine it was not written
	// on.
	if local, ok := observed.version(nix.System()); ok {
		resp.Version = local
	}

	for _, system := range systems {
		found, ok := observed[system]
		if !ok || !found.Available() || found.Version != resp.Version {
			// The row's commit is not where every system has this version. An
			// index row spans the commits one system saw a version over, and
			// platform support moves inside that span: dumb-init 1.2.5 runs
			// from 2021 to now, and aarch64-darwin gained it somewhere in the
			// middle. Ask where this system has it instead of giving up.
			elsewhere, err := index.SystemsForVersion(ctx, row.AttributePath, system, resp.Version)
			if err != nil {
				slog.Warn("resolve: could not look for the version on this system",
					"attr", row.AttributePath, "system", system, "err", err)
				continue
			}
			found, ok = firstAvailable(elsewhere)
			if !ok {
				continue
			}
		}
		info := resp.Systems[system]
		info.FlakeInstallable = flake.Installable{
			Ref: flake.Ref{
				Type:  flake.TypeGitHub,
				Owner: "NixOS",
				Repo:  "nixpkgs",
				Rev:   found.CommitHash,
			},
			AttrPath: row.AttributePath,
		}
		info.LastUpdated = found.CommitDate
		for _, output := range found.Outputs {
			if output.Path == "" {
				continue
			}
			outputs, elem := appendZero(info.Outputs)
			elem.Name = output.Name
			elem.Path = output.Path
			elem.Default = output.Default
			info.Outputs = outputs
		}
		resp.Systems[system] = info
	}

	// /v2/resolve may not answer with no systems: Devbox reads the response
	// through a selector that discards the "no systems" error and would write a
	// lock entry with an empty installable.
	if len(resp.Systems) == 0 {
		info := resp.Systems[nix.System()]
		info.FlakeInstallable = installable
		info.LastUpdated = row.FirstCommitDate
		resp.Systems[nix.System()] = info
	}
	return resp
}

// firstAvailable returns the first observation that can actually be installed.
func firstAvailable(observations []nxv.SystemObservation) (nxv.SystemObservation, bool) {
	for _, observation := range observations {
		if observation.Available() {
			return observation, true
		}
	}
	return nxv.SystemObservation{}, false
}

// appendZero grows a slice by one zero value and returns it with a pointer to
// that element.
//
// The response's output type has no name, so no literal can construct one and
// no closure can take one as a parameter. Growing the slice and filling the new
// element through an inferred pointer keeps the field assignments at the call
// site, where the compiler knows the type, and keeps the upstream searcher
// types unchanged.
func appendZero[T any](slice []T) ([]T, *T) {
	slice = append(slice, *new(T))
	return slice, &slice[len(slice)-1]
}

// resolveExactPerSystem resolves a version that only one system ever had.
//
// A Darwin `jdk25@25.0.3` is in no index row: at every commit in that range the
// snapshot system saw an openjdk version. The index can still find it, by
// evaluating the commits where it knows the attribute path existed, and the
// answer names the commit to pin. Returns [nxv.ErrNotFound] when no candidate
// attribute path had that version on this system.
func resolveExactPerSystem(ctx context.Context, index *nxv.Client, name, version string) (
	*nxv.SystemObservation, error,
) {
	system := nix.System()
	for _, attr := range AttrCandidates(name, version) {
		rows, err := index.SystemsForVersion(ctx, attr, system, version)
		if errors.Is(err, nxv.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.Available() {
				return &row, nil
			}
		}
	}
	return nil, nxv.ErrNotFound
}

// rowFromObservation presents an observation as an index row, so a per-system
// resolution serializes through the same path as a snapshot one.
//
// The commit bounds are the observation's single commit: it is one evaluation,
// not a range, and widening it would claim commits nothing evaluated.
func rowFromObservation(obs *nxv.SystemObservation) *nxv.PackageVersion {
	return &nxv.PackageVersion{
		Name:            obs.StoreName,
		Version:         obs.Version,
		AttributePath:   obs.AttributePath,
		FirstCommitHash: obs.CommitHash,
		FirstCommitDate: obs.CommitDate,
		LastCommitHash:  obs.CommitHash,
		LastCommitDate:  obs.CommitDate,
	}
}

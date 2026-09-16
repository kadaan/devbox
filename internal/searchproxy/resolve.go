// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package searchproxy

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"

	"go.jetify.com/devbox/internal/nxv"
	"go.jetify.com/devbox/internal/searcher"
)

// latestConstraint is the version Devbox sends for an unpinned package.
const latestConstraint = "latest"

// versionPrefix matches the constraint forms that select a version rather than
// name one: a major, or a major and a minor.
var versionPrefix = regexp.MustCompile(`^\d+(\.\d+)?$`)

// DefaultSystems are the systems reported when none are configured: the four
// Devbox supports. nixpkgs lists dozens per package, including targets Devbox
// cannot build for, and reporting them all makes `devbox search` unreadable.
var DefaultSystems = []string{
	"aarch64-darwin",
	"aarch64-linux",
	"x86_64-darwin",
	"x86_64-linux",
}

func resolveV2(w http.ResponseWriter, r *http.Request, index *nxv.Client, systems []string) {
	handleResolve(w, r, index, func(ctx context.Context, row *nxv.PackageVersion) searcher.ResolveResponse {
		return resolveResponse(ctx, index, row, systems)
	})
}

func handleResolve[T any](w http.ResponseWriter, r *http.Request, index *nxv.Client,
	serialize func(context.Context, *nxv.PackageVersion) T,
) {
	name := r.URL.Query().Get("name")
	version := r.URL.Query().Get("version")
	if name == "" || version == "" {
		writeError(w, http.StatusBadRequest, "name and version are required")
		return
	}

	row, err := resolve(r.Context(), index, name, version)
	switch {
	case errors.Is(err, nxv.ErrNotFound):
		writeError(w, http.StatusNotFound, name+"@"+version+" is not in the index")
	case err != nil:
		slog.Error("resolve", "path", r.URL.Path, "name", name, "version", version, "err", err)
		writeError(w, http.StatusBadGateway, "index lookup failed")
	default:
		writeJSON(w, http.StatusOK, serialize(r.Context(), row))
	}
}

// resolve returns the index row a Devbox name and constraint select, where the
// constraint is an exact version, an "N" or "N.N" prefix, or "latest". It
// returns [nxv.ErrNotFound] when no candidate attribute path carries a match.
func resolve(ctx context.Context, index *nxv.Client, name, constraint string) (
	*nxv.PackageVersion, error,
) {
	if constraint != latestConstraint {
		row, err := resolveExact(ctx, index, name, constraint)
		if !errors.Is(err, nxv.ErrNotFound) {
			return row, err
		}

		// A "25" or "25.0" is a prefix to select with, not a version to look
		// for: no package is at "25" exactly, and asking the index to find one
		// per system would evaluate every commit of the attribute path to
		// answer nothing. Those go to the constraint path, which selects a row
		// and then gets that row's per-system version.
		if versionPrefix.MatchString(constraint) {
			return resolveConstraint(ctx, index, name, constraint)
		}

		// A full version this machine's system saw but the snapshot system
		// never did is in no index row, so an exact miss is not yet an answer.
		// This is the pin a Darwin `devbox add` writes: jdk25@25.0.3 exists
		// only as a per-system observation.
		observed, perSystemErr := resolveExactPerSystem(ctx, index, name, constraint)
		if perSystemErr == nil {
			return rowFromObservation(observed), nil
		}
		if !errors.Is(perSystemErr, nxv.ErrNotFound) {
			return nil, perSystemErr
		}
		return nil, err
	}
	return resolveConstraint(ctx, index, name, constraint)
}

func resolveExact(ctx context.Context, index *nxv.Client, name, version string) (
	*nxv.PackageVersion, error,
) {
	return firstCandidate(name, version, func(attr string) (*nxv.PackageVersion, error) {
		return index.Version(ctx, attr, version)
	})
}

func resolveConstraint(ctx context.Context, index *nxv.Client, name, constraint string) (
	*nxv.PackageVersion, error,
) {
	head, err := index.IndexCommit(ctx)
	if err != nil {
		return nil, err
	}
	if head == "" {
		return nil, errors.New("nxv index reports no head commit")
	}

	prefix := constraint
	if constraint == latestConstraint {
		prefix = ""
	}
	row, err := firstCandidate(name, constraint, func(attr string) (*nxv.PackageVersion, error) {
		rows, err := index.Versions(ctx, attr)
		if err != nil {
			return nil, err
		}
		return selectVersion(rows, prefix, head), nil
	})
	if constraint != latestConstraint || (err != nil && !errors.Is(err, nxv.ErrNotFound)) {
		return row, err
	}

	// A candidate that is not at head does not answer "latest": nixpkgs still
	// defines the attribute path "python", but it is the retired Python 2 line,
	// whose newest row is 2.7.18 from 2020. Look for a family member that is
	// current, and keep the stale row only when there is none.
	if row != nil && row.LastCommitHash == head {
		return row, nil
	}
	family, familyErr := resolveLatestFamily(ctx, index, name, head)
	if errors.Is(familyErr, nxv.ErrNotFound) {
		return row, err
	}
	if familyErr != nil {
		return nil, familyErr
	}
	return betterRow(row, family, head), nil
}

// resolveLatestFamily answers "latest" for a name whose versions live under a
// family attribute path, such as python (python314) or ruby (ruby_3_2).
//
// AttrCandidates cannot reach those here: "latest" carries no version to derive
// a suffix from, so the only candidate it yields is the bare name. This asks the
// index which attribute paths exist for the name and keeps the ones whose path
// is a mechanical family member, which is a shape test rather than a pname
// lookup — a hit matching only on pname or description is never accepted.
func resolveLatestFamily(ctx context.Context, index *nxv.Client, name, head string) (
	*nxv.PackageVersion, error,
) {
	hits, err := index.Search(ctx, name)
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	var best *nxv.PackageVersion
	for i := range hits {
		attr := hits[i].AttributePath
		if seen[attr] || !IsFamilyAttr(name, attr) {
			continue
		}
		seen[attr] = true

		rows, err := index.Versions(ctx, attr)
		if errors.Is(err, nxv.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		best = betterRow(best, selectVersion(rows, "", head), head)
	}
	if best == nil {
		return nil, nxv.ErrNotFound
	}
	return best, nil
}

// betterRow returns whichever row "latest" should prefer, in order: one still at
// head over one that is not; then the shorter attribute path; then the greater
// version.
//
// The shorter path wins because nixpkgs names a family's current member with the
// least specific suffix — python3 is 3.14.7 while python315 is a release
// candidate, and nodejs is the default line while nodejs_25 is opt-in. Taking
// the greater version instead would install that candidate. It also keeps this
// consistent with AttrCandidates, which already tries the least specific
// attribute path first.
func betterRow(best, next *nxv.PackageVersion, head string) *nxv.PackageVersion {
	switch {
	case next == nil:
		return best
	case best == nil:
		return next
	}
	bestAtHead, nextAtHead := best.LastCommitHash == head, next.LastCommitHash == head
	if bestAtHead != nextAtHead {
		if nextAtHead {
			return next
		}
		return best
	}
	if len(best.AttributePath) != len(next.AttributePath) {
		if len(next.AttributePath) < len(best.AttributePath) {
			return next
		}
		return best
	}
	if CompareVersions(next.Version, best.Version) > 0 {
		return next
	}
	return best
}

// firstCandidate returns the first row lookup finds among the attribute paths
// AttrCandidates derives, and [nxv.ErrNotFound] when no candidate carries one.
func firstCandidate(name, constraint string,
	lookup func(attr string) (*nxv.PackageVersion, error),
) (*nxv.PackageVersion, error) {
	for _, attr := range AttrCandidates(name, constraint) {
		row, err := lookup(attr)
		if errors.Is(err, nxv.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if row != nil {
			return row, nil
		}
	}
	return nil, nxv.ErrNotFound
}

// selectVersion returns the row a constraint selects: among the rows matching
// prefix, the greatest of those whose range still extends to head, or the
// greatest of them all when none does.
func selectVersion(rows []nxv.PackageVersion, prefix, head string) *nxv.PackageVersion {
	var best, bestAtHead *nxv.PackageVersion
	for i := range rows {
		row := &rows[i]
		if !hasVersionPrefix(row.Version, prefix) {
			continue
		}
		if best == nil || CompareVersions(row.Version, best.Version) > 0 {
			best = row
		}
		if head == "" || row.LastCommitHash != head {
			continue
		}
		if bestAtHead == nil || CompareVersions(row.Version, bestAtHead.Version) > 0 {
			bestAtHead = row
		}
	}
	if bestAtHead != nil {
		return bestAtHead
	}
	return best
}

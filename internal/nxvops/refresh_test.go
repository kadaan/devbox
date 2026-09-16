// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package nxvops_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jetify.com/devbox/internal/nxvops"
)

func TestRefreshRunsIncrementalIndex(t *testing.T) {
	binary, argv := stubNxv(t)

	require.NoError(t, nxvops.Refresh(t.Context(), binary, "", []string{"--since", "2026-01-01"}))

	args := argv()
	require.NotEmpty(t, args)
	assert.Equal(t, "index", args[0], "no global flags when no database is named")
	assert.NotContains(t, args, "--full")
	assert.Equal(t, []string{"--since", "2026-01-01"}, args[len(args)-2:],
		"passthrough args go last, after the channels")
	for _, channel := range nxvops.Channels {
		assert.Subset(t, args, []string{"--channel", channel})
	}
}

func TestRefreshRefusesReplanningFlags(t *testing.T) {
	for _, flag := range []string{"--full", "--full=true"} {
		t.Run(flag, func(t *testing.T) {
			binary, argv := stubNxv(t)

			err := nxvops.Refresh(t.Context(), binary, "", []string{flag})

			require.Error(t, err)
			assert.Contains(t, err.Error(), flag)
			assert.Empty(t, argv(), "nxv must not run at all")
		})
	}
}

// --backfill-evals only adds the pre-2020 releases to the worklist that are
// otherwise filtered out; it re-plans nothing. Refusing it would leave the
// releases the report calls "unreachable without --backfill-evals" unreachable
// through this command.
func TestRefreshAllowsBackfillEvals(t *testing.T) {
	binary, argv := stubNxv(t)

	require.NoError(t, nxvops.Refresh(t.Context(), binary, "", []string{"--backfill-evals"}))

	assert.Equal(t, "--backfill-evals", argv()[len(argv())-1])
}

// --db-path is a global nxv flag, so it has to precede the subcommand. Passing
// it through after `index` makes nxv exit 2 with "unexpected argument".
func TestRefreshPutsTheDatabasePathBeforeTheSubcommand(t *testing.T) {
	binary, argv := stubNxv(t)

	require.NoError(t, nxvops.Refresh(t.Context(), binary, "/tmp/nxv.db", nil))

	args := argv()
	require.GreaterOrEqual(t, len(args), 3)
	assert.Equal(t, []string{"--db-path", "/tmp/nxv.db", "index"}, args[:3])
}

// Only the unstable channels are ingested. A nixos-* stable branch is frozen at
// its branch-point size and keeps releasing for months, so it trips nxv's
// date-keyed package-count floor and its 'nh' sentinel, failing releases and
// reporting the index unhealthy -- and it carries backports rather than the new
// versions an index is for.
func TestChannelsAreTheUnstableOnesOnly(t *testing.T) {
	assert.Equal(t,
		[]string{"nixpkgs-unstable", "nixos-unstable-small"},
		nxvops.Channels)
}

func TestSyncDownloadsOverTheNamedDatabase(t *testing.T) {
	binary, argv := stubNxv(t)

	require.NoError(t, nxvops.Sync(t.Context(), binary, "/tmp/nxv.db", false, nil))

	assert.Equal(t, []string{"--db-path", "/tmp/nxv.db", "sync"}, argv())
}

func TestSyncForcesWhenAsked(t *testing.T) {
	binary, argv := stubNxv(t)

	require.NoError(t, nxvops.Sync(t.Context(), binary, "", true, []string{"--skip-verify"}))

	assert.Equal(t, []string{"sync", "--force", "--skip-verify"}, argv())
}

// devbox's error handler prints "There was an internal error ... consider
// reporting it" for any error carrying an *exec.ExitError. nxv exiting non-zero
// is nxv's failure to report, not a devbox fault, so the chain must not hold one.
func TestNxvFailureIsNotReportedAsAnInternalFault(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(binary string) error
	}{
		{"refresh", func(binary string) error {
			return nxvops.Refresh(t.Context(), binary, "", nil)
		}},
		{"sync", func(binary string) error {
			return nxvops.Sync(t.Context(), binary, "", false, nil)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.run("/usr/bin/false")

			require.Error(t, err)
			var exitErr *exec.ExitError
			assert.False(t, errors.As(err, &exitErr),
				"error chain must not carry *exec.ExitError: %v", err)
			assert.Contains(t, err.Error(), "exited with status 1")
		})
	}
}

// Building over a populated index would silently be an ordinary incremental
// refresh rather than the rebuild the caller asked for.
func TestBuildRefusesAnExistingIndex(t *testing.T) {
	binary, argv := stubNxv(t)
	existing := filepath.Join(t.TempDir(), "index.db")
	require.NoError(t, os.WriteFile(existing, []byte("not empty"), 0o600))

	err := nxvops.Build(t.Context(), binary, existing, false, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "already holds an index")
	assert.Empty(t, argv(), "nxv must not run at all")
}

func TestBuildNeedsAnExplicitPath(t *testing.T) {
	binary, argv := stubNxv(t)

	err := nxvops.Build(t.Context(), binary, "", false, nil)

	require.Error(t, err)
	assert.Empty(t, argv())
}

func TestBuildIngestsIntoANewIndex(t *testing.T) {
	binary, argv := stubNxv(t)
	fresh := filepath.Join(t.TempDir(), "index.db")

	require.NoError(t, nxvops.Build(t.Context(), binary, fresh, true, []string{"--max-releases", "1"}))

	args := argv()
	assert.Equal(t, []string{"--db-path", fresh, "index"}, args[:3])
	assert.Subset(t, args, []string{"--backfill-evals"})
	assert.Equal(t, []string{"--max-releases", "1"}, args[len(args)-2:])
}

func TestPublishWritesToTheOutputDirectory(t *testing.T) {
	binary, argv := stubNxv(t)

	require.NoError(t, nxvops.Publish(t.Context(), binary, "/tmp/nxv.db", "/tmp/out",
		"https://example.s3.amazonaws.com/nxv", []string{"--sign"}))

	assert.Equal(t, []string{
		"--db-path", "/tmp/nxv.db", "publish", "--output", "/tmp/out",
		"--url-prefix", "https://example.s3.amazonaws.com/nxv", "--sign",
	}, argv())
}

func TestPublishNeedsAnOutputDirectory(t *testing.T) {
	binary, argv := stubNxv(t)

	require.Error(t, nxvops.Publish(t.Context(), binary, "", "", "", nil))
	assert.Empty(t, argv())
}

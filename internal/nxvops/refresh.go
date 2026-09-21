// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package nxvops

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"

	"go.jetify.com/devbox/internal/redact"
)

// Channels are the nixpkgs channels the index ingests, which are nxv's own
// defaults: nixpkgs-unstable is the historical spine back to 2016 and
// nixos-unstable-small is the currency channel.
//
// The nixos-* stable branches are deliberately not ingested. nxv's design notes
// give the reason -- "all package development happens in unstable; stable
// branches only backport security fixes" -- and measurement agreed: ingesting
// 136 nixos-21.11 and 181 nixos-22.05 releases added 81,468 rows of backports
// and still did not produce the python39 3.9.5-3.9.12 versions they were added
// for. They also cost a health signal. nxv's snapshot gates key on a release's
// date against unstable's growth curve, so a frozen branch trips them: the
// package-count floor ladders by calendar year while a stable branch stays at
// its branch-point size, and the 'nh' sentinel is enforced from 2024 for a
// package that only ever landed in unstable. That failed 93 releases, and every
// failure retries on backoff and reports the index UNHEALTHY until it exhausts
// its attempts.
var Channels = []string{
	"nixpkgs-unstable",
	"nixos-unstable-small",
}

// replanningFlags re-queue every known release. On a bootstrapped database the
// pre-2020 releases they queue can never be ingested without --backfill-evals,
// so the watermark never settles again and the index stays unhealthy.
//
// Only --full does this. --backfill-evals is not here despite being slow and
// sounding drastic: it only adds the pre-2020 releases to the worklist that are
// otherwise filtered out, which is how the "unreachable without
// --backfill-evals" releases the report names are settled.
var replanningFlags = []string{"--full"}

// Refresh runs the incremental `nxv index` over [Channels], passing args through
// to it, and errors without running nxv when args hold a re-planning flag.
//
// dbPath may be empty to let nxv use its own default index location. It is a
// global nxv flag rather than an `index` one, so it cannot be passed through
// args: nxv rejects it after the subcommand name.
func Refresh(ctx context.Context, binary, dbPath string, args []string) error {
	for _, arg := range args {
		for _, flag := range replanningFlags {
			if arg == flag || strings.HasPrefix(arg, flag+"=") {
				return redact.Errorf(
					"%s re-plans the index and is forbidden here; rebuild into a fresh database instead",
					redact.Safe(arg),
				)
			}
		}
	}

	nxvArgs := []string{}
	if dbPath != "" {
		nxvArgs = append(nxvArgs, "--db-path", dbPath)
	}
	nxvArgs = append(nxvArgs, "index")
	for _, channel := range Channels {
		nxvArgs = append(nxvArgs, "--channel", channel)
	}
	nxvArgs = append(nxvArgs, args...)

	cmd := exec.CommandContext(ctx, binary, nxvArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return runNxv(cmd, "index")
}

// Sync downloads a prebuilt index over dbPath, replacing whatever is there.
//
// This is the bootstrap: building the same coverage locally takes hours, and
// most of it is archaeology of channel releases that will never change again.
// Pass force to overwrite an index that is already current.
func Sync(ctx context.Context, binary, dbPath string, force bool, args []string) error {
	nxvArgs := []string{}
	if dbPath != "" {
		nxvArgs = append(nxvArgs, "--db-path", dbPath)
	}
	nxvArgs = append(nxvArgs, "sync")
	if force {
		nxvArgs = append(nxvArgs, "--force")
	}
	nxvArgs = append(nxvArgs, args...)

	cmd := exec.CommandContext(ctx, binary, nxvArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return runNxv(cmd, "sync")
}

// runNxv runs cmd and reports a non-zero exit without wrapping the underlying
// *exec.ExitError.
//
// Wrapping it would put an *exec.ExitError in the error chain, and devbox's
// error handler treats any error carrying one as an internal fault -- printing
// "There was an internal error ... consider reporting it" over a failure that
// is nxv's to report and has already printed its own diagnostics.
func runNxv(cmd *exec.Cmd, subcommand string) error {
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return redact.Errorf("nxv %s exited with status %d",
				redact.Safe(subcommand), redact.Safe(exitErr.ExitCode()))
		}
		return redact.Errorf("run nxv %s: %w", redact.Safe(subcommand), redact.Safe(err))
	}
	return nil
}

// Build ingests every channel release into dbPath from nothing.
//
// It refuses to run against an existing index rather than deleting one: this is
// the slow path, measured in hours, and an accidental run against a populated
// database would silently be an ordinary incremental refresh instead of the
// rebuild the caller asked for.
//
// backfillEvals adds the pre-2020 nix-env era, which needs nix and takes hours
// more. Without it the index starts at the packages.json era (~2020).
func Build(ctx context.Context, binary, dbPath string, backfillEvals bool, args []string) error {
	if dbPath == "" {
		return redact.Errorf("building from scratch needs an explicit index path")
	}
	if _, err := os.Stat(dbPath); err == nil {
		return redact.Errorf(
			"%s already holds an index; remove it or build somewhere else",
			redact.Safe(dbPath),
		)
	} else if !errors.Is(err, os.ErrNotExist) {
		return redact.Errorf("check %s: %w", redact.Safe(dbPath), redact.Safe(err))
	}

	if backfillEvals {
		args = append([]string{"--backfill-evals"}, args...)
	}
	return Refresh(ctx, binary, dbPath, args)
}

// Publish writes the compressed index, bloom filter and manifest that [Sync]
// downloads, so an index can be served from somewhere other than nxv's own
// releases.
func Publish(ctx context.Context, binary, dbPath, outDir, urlPrefix string, args []string) error {
	if outDir == "" {
		return redact.Errorf("publishing needs an output directory")
	}

	nxvArgs := []string{}
	if dbPath != "" {
		nxvArgs = append(nxvArgs, "--db-path", dbPath)
	}
	nxvArgs = append(nxvArgs, "publish", "--output", outDir)
	if urlPrefix != "" {
		nxvArgs = append(nxvArgs, "--url-prefix", urlPrefix)
	}
	nxvArgs = append(nxvArgs, args...)

	cmd := exec.CommandContext(ctx, binary, nxvArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return runNxv(cmd, "publish")
}

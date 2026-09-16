// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package lock

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"go.jetify.com/devbox/internal/build"
	"go.jetify.com/devbox/internal/cachehash"
	"go.jetify.com/devbox/internal/cuecfg"
	"go.jetify.com/devbox/internal/nix"
)

var ignoreShellMismatch = false

// stateHashFile is a non-shared lock file that helps track the state of the
// local devbox environment. It contains hashes that may not be the same across
// machines (e.g. manifest hash).
// When we do implement a shared lock file, it may contain some shared fields
// with this one but not all.
type stateHashFile struct {
	ConfigHash    string `json:"config_hash"`
	DevboxVersion string `json:"devbox_version"`
	// fish has different generated scripts so we need to recompute them if user
	// changes shell.
	IsFish                 bool   `json:"is_fish"`
	LockFileHash           string `json:"lock_file_hash"`
	NixPrintDevEnvHash     string `json:"nix_print_dev_env_hash"`
	NixProfileManifestHash string `json:"nix_profile_manifest_hash"`
}

type UpdateStateHashFileArgs struct {
	ProjectDir string
	ConfigHash string
	// IsFish is an arg because in the future we may allow the user
	// to specify shell in devbox.json which should be passed in here.
	IsFish bool
}

func UpdateAndSaveStateHashFile(args UpdateStateHashFileArgs) error {
	newLock, err := getCurrentStateHash(args)
	if err != nil {
		return err
	}

	return cuecfg.WriteFile(stateHashFilePath(args.ProjectDir), newLock)
}

// SetIgnoreShellMismatch is used to disable the shell comparison when checking
// if the state is up to date. This is useful when we don't load shellrc (e.g. running)
func SetIgnoreShellMismatch(ignore bool) {
	ignoreShellMismatch = ignore
}

func isStateUpToDate(args UpdateStateHashFileArgs) (bool, error) {
	filesystemStateHash, err := readStateHashFile(args.ProjectDir)
	if err != nil {
		return false, err
	}
	newStateHash, err := getCurrentStateHash(args)
	if err != nil {
		return false, err
	}

	if ignoreShellMismatch {
		filesystemStateHash.IsFish = newStateHash.IsFish
	}

	return *filesystemStateHash == *newStateHash, nil
}

func readStateHashFile(projectDir string) (*stateHashFile, error) {
	hashFile := &stateHashFile{}
	err := cuecfg.ParseFile(stateHashFilePath(projectDir), hashFile)
	if errors.Is(err, fs.ErrNotExist) {
		return hashFile, nil
	}
	if err != nil {
		return nil, err
	}
	return hashFile, nil
}

func getCurrentStateHash(args UpdateStateHashFileArgs) (*stateHashFile, error) {
	nixHash, err := manifestHash(args.ProjectDir)
	if err != nil {
		return nil, err
	}

	printDevEnvCacheHash, err := printDevEnvCacheHash(args.ProjectDir)
	if err != nil {
		return nil, err
	}

	lockfileHash, err := getLockfileHash(args.ProjectDir)
	if err != nil {
		return nil, err
	}

	newLock := &stateHashFile{
		ConfigHash:             args.ConfigHash,
		DevboxVersion:          build.Version,
		IsFish:                 args.IsFish,
		LockFileHash:           lockfileHash,
		NixPrintDevEnvHash:     printDevEnvCacheHash,
		NixProfileManifestHash: nixHash,
	}

	return newLock, nil
}

// devEnvHasCollectedStorePaths reports whether the cached dev environment
// names store paths that are no longer in the store.
//
// The hashes above cannot see this. They hash the cache's bytes, and a garbage
// collection changes the store without changing the file, so every hash still
// matches while the environment it describes has holes in it. Checking the
// store is what turns that silent breakage into an ordinary recompute.
func devEnvHasCollectedStorePaths(ctx context.Context, projectDir string) (bool, error) {
	data, err := os.ReadFile(printDevEnvCachePath(projectDir))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	missing, err := nix.MissingStorePaths(ctx, nix.StorePaths(data))
	if err != nil {
		// Nix could not answer, so we cannot tell a collected path from one this
		// check parsed wrongly. Reporting "collected" on that ambiguity would
		// recompute the environment on every command, which is worse than
		// leaving the hashes to decide as they did before this check existed.
		slog.Debug("could not check dev env store paths", "err", err)
		return false, nil //nolint:nilerr
	}
	return len(missing) > 0, nil
}

func stateHashFilePath(projectDir string) string {
	return filepath.Join(projectDir, ".devbox", "state.json")
}

func manifestHash(profileDir string) (string, error) {
	return cachehash.JSONFile(filepath.Join(profileDir, ".devbox/nix/profile/default/manifest.json"))
}

func printDevEnvCachePath(projectDir string) string {
	return filepath.Join(projectDir, ".devbox/.nix-print-dev-env-cache")
}

func printDevEnvCacheHash(profileDir string) (string, error) {
	return cachehash.JSONFile(printDevEnvCachePath(profileDir))
}

func getLockfileHash(projectDir string) (string, error) {
	return cachehash.JSONFile(lockFilePath(projectDir))
}

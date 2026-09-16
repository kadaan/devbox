// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package nxvops

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"

	"go.jetify.com/devbox/internal/nix"
	"go.jetify.com/devbox/internal/redact"
)

// DefaultInstallable is the nxv build a refresh uses when given no executable.
//
// It is the indexer variant on purpose: nxv's `index` subcommand sits behind an
// `indexer` Cargo feature that is not in its default feature set, so nxv's
// published release binaries cannot refresh an index at all. Building it is the
// only way to get one.
//
// Pinned to a revision rather than the v0.7.3 tag, which could be moved to
// different code under the same name.
const DefaultInstallable = "github:utensils/nxv/82413f05712fd67069e45f82e8256feaf546271d#nxv-indexer"

// ResolveBinary returns the path to an nxv executable, building it with nix when
// binary is empty so that operating an index needs no local nxv install.
//
// The first build compiles nxv from source, which takes a while; nix keeps the
// result, so later runs resolve from the store.
func ResolveBinary(ctx context.Context, binary, installable string) (string, error) {
	if binary != "" {
		return binary, nil
	}
	if installable == "" {
		installable = DefaultInstallable
	}

	out, err := nix.Command("build", "--no-link", "--print-out-paths", installable).
		Output(ctx)
	if err != nil {
		return "", redact.Errorf("build %s with nix: %w",
			redact.Safe(installable), redact.Safe(err))
	}

	// --print-out-paths prints one store path per output of the installable.
	paths := strings.Fields(string(out))
	if len(paths) == 0 {
		return "", redact.Errorf("nix built %s but printed no store path",
			redact.Safe(installable))
	}

	path := filepath.Join(paths[len(paths)-1], "bin", "nxv")
	if _, err := exec.LookPath(path); err != nil {
		return "", redact.Errorf("%s does not provide bin/nxv: %w",
			redact.Safe(installable), redact.Safe(err))
	}
	return path, nil
}

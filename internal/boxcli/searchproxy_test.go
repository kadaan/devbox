// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package boxcli

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubNxv writes an executable standing in for nxv that records its arguments
// and prints stdout, and returns its path plus a reader for those arguments.
func stubNxv(t *testing.T, stdout string) (string, func() []string) {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "nxv")
	argv := filepath.Join(dir, "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argv + "\n"
	if stdout != "" {
		script += "printf '%s' " + strconv.Quote(stdout) + "\n"
	}
	require.NoError(t, os.WriteFile(binary, []byte(script), 0o755))

	return binary, func() []string {
		data, err := os.ReadFile(argv)
		if os.IsNotExist(err) {
			return nil
		}
		require.NoError(t, err)
		return strings.Fields(string(data))
	}
}

// runSearchProxyIndex executes the search-index command tree with args, keeping its
// output off the test log.
func runSearchProxyIndex(t *testing.T, args ...string) error {
	t.Helper()
	command := searchProxyIndexCmd()
	command.SetArgs(args)
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	return command.ExecuteContext(t.Context())
}

func TestSearchProxyIndexRefreshRefusesAReplanningFlag(t *testing.T) {
	binary, _ := stubNxv(t, "")

	err := runSearchProxyIndex(t, "refresh", "--nxv", binary, "--", "--full")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "--full")
}

func TestSearchProxyIndexRefreshPassesNxvArgs(t *testing.T) {
	binary, argv := stubNxv(t, "")

	require.NoError(t, runSearchProxyIndex(t,
		"refresh", "--nxv", binary, "--", "--database", "/tmp/nxv.db"))

	assert.Equal(t, []string{"--database", "/tmp/nxv.db"}, argv()[len(argv())-2:])
}

// health reads a report rather than running nxv: `nxv index` ingests, so a
// health check that ran it would mutate the index it is meant to observe.
func writeReport(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "report.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestSearchProxyIndexHealthPassesOnAHealthyReport(t *testing.T) {
	path := writeReport(t, `{"healthy":true,"head_lag_hours":7}`)

	assert.NoError(t, runSearchProxyIndex(t, "health", "--report", path))
}

func TestSearchProxyIndexHealthFailsBeyondMaxLag(t *testing.T) {
	path := writeReport(t, `{"healthy":true,"head_lag_hours":48}`)

	err := runSearchProxyIndex(t, "health", "--report", path, "--max-lag", "24h")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "head lag")
}

func TestSearchProxyIndexHealthRequiresAReport(t *testing.T) {
	assert.Error(t, runSearchProxyIndex(t, "health"))
}

func TestSearchProxyIndexRejectsAnUnknownSubcommand(t *testing.T) {
	assert.Error(t, runSearchProxyIndex(t, "reindex"))
}

func TestSearchProxyTreeIsRegisteredOnTheRootCommand(t *testing.T) {
	for _, path := range [][]string{
		{"search-proxy"},
		{"search-proxy", "serve"},
		{"search-proxy", "index"},
		{"search-proxy", "index", "refresh"},
		{"search-proxy", "index", "health"},
	} {
		found, _, err := RootCmd().Find(path)
		require.NoError(t, err)
		assert.Equal(t, path[len(path)-1], found.Name(),
			"devbox %s should exist", strings.Join(path, " "))
	}
}

// The subcommands must not shadow `devbox search <pkg>`, which takes the
// package name as a positional argument.
func TestSearchStillResolvesToTheSearchCommand(t *testing.T) {
	found, args, err := RootCmd().Find([]string{"search", "python"})

	require.NoError(t, err)
	assert.Equal(t, "search", found.Name())
	assert.Equal(t, []string{"python"}, args)
}

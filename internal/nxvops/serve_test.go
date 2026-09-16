// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package nxvops_test

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jetify.com/devbox/internal/nxvops"
)

// The tests stand in for nxv with this test binary itself, re-executed with
// NXVOPS_FAKE_NXV set, so they need neither nxv nor nix.
func TestMain(m *testing.M) {
	switch os.Getenv("NXVOPS_FAKE_NXV") {
	case "":
		os.Exit(m.Run())
	case "exit":
		os.Exit(1)
	default:
		fakeNxv()
	}
}

func fakeNxv() {
	port := ""
	for i, arg := range os.Args {
		if arg == "--port" && i+1 < len(os.Args) {
			port = os.Args[i+1]
		}
	}
	if path := os.Getenv("NXVOPS_FAKE_NXV_ARGV"); path != "" {
		_ = os.WriteFile(path, []byte(strings.Join(os.Args[1:], " ")), 0o600)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	_ = http.ListenAndServe("127.0.0.1:"+port, mux) //nolint:gosec
}

func TestStartServerWaitsUntilHealthyThenStops(t *testing.T) {
	t.Setenv("NXVOPS_FAKE_NXV", "serve")

	server, err := nxvops.StartServer(t.Context(), os.Args[0], "", false)
	require.NoError(t, err)
	require.NotEmpty(t, server.URL)

	// StartServer returning means the server is already answering.
	resp, err := http.Get(server.URL + "/api/v1/health") //nolint:noctx
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	require.NoError(t, server.Stop())

	_, err = http.Get(server.URL + "/api/v1/health") //nolint:noctx
	assert.Error(t, err, "the server should be gone after Stop")
}

// A server that exits immediately must fail fast rather than waiting out the
// whole start timeout, which is minutes long to allow for a cold index.
func TestStartServerFailsWhenTheProcessExits(t *testing.T) {
	t.Setenv("NXVOPS_FAKE_NXV", "exit")

	start := time.Now()
	server, err := nxvops.StartServer(t.Context(), os.Args[0], "", false)

	require.Error(t, err)
	assert.Nil(t, server)
	assert.Contains(t, err.Error(), "exited before becoming healthy")
	assert.Less(t, time.Since(start), 30*time.Second,
		"should not wait out the start timeout")
}

func TestStartServerPassesTheDatabasePath(t *testing.T) {
	argv := t.TempDir() + "/argv"
	t.Setenv("NXVOPS_FAKE_NXV", "serve")
	t.Setenv("NXVOPS_FAKE_NXV_ARGV", argv)

	server, err := nxvops.StartServer(t.Context(), os.Args[0], "/tmp/index.db", false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Stop() })

	recorded, err := os.ReadFile(argv)
	require.NoError(t, err)
	assert.Contains(t, string(recorded), "--db-path /tmp/index.db")
	assert.Contains(t, string(recorded), "serve --host 127.0.0.1 --port")
}

func TestStartServerAsksForPerSystemEvaluationWhenEnabled(t *testing.T) {
	argv := t.TempDir() + "/argv"
	t.Setenv("NXVOPS_FAKE_NXV", "serve")
	t.Setenv("NXVOPS_FAKE_NXV_ARGV", argv)

	server, err := nxvops.StartServer(t.Context(), os.Args[0], "", true)
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Stop() })

	recorded, err := os.ReadFile(argv)
	require.NoError(t, err)
	assert.Contains(t, string(recorded), "--eval-systems")
}

func TestStartServerOmitsPerSystemEvaluationWhenDisabled(t *testing.T) {
	argv := t.TempDir() + "/argv"
	t.Setenv("NXVOPS_FAKE_NXV", "serve")
	t.Setenv("NXVOPS_FAKE_NXV_ARGV", argv)

	server, err := nxvops.StartServer(t.Context(), os.Args[0], "", false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Stop() })

	recorded, err := os.ReadFile(argv)
	require.NoError(t, err)
	assert.NotContains(t, string(recorded), "--eval-systems")
}

func TestStartServerOmitsTheDatabasePathWhenEmpty(t *testing.T) {
	argv := t.TempDir() + "/argv"
	t.Setenv("NXVOPS_FAKE_NXV", "serve")
	t.Setenv("NXVOPS_FAKE_NXV_ARGV", argv)

	server, err := nxvops.StartServer(t.Context(), os.Args[0], "", false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Stop() })

	recorded, err := os.ReadFile(argv)
	require.NoError(t, err)
	assert.NotContains(t, string(recorded), "--db-path",
		"nxv should fall back to its own default index location")
}

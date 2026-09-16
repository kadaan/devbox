// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package boxcli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jetify.com/devbox/internal/nxv"
	"go.jetify.com/devbox/internal/searchproxy"
)

const helloRows = `{"data":[{"attribute_path":"hello","name":"hello","version":"2.12.1",
	"first_commit_hash":"3d46470bb3030020f7e1361f33514854f5bfa86d",
	"first_commit_date":"2024-05-01T00:00:00Z",
	"platforms":["arm64-macOS","x86_64-linux"]}]}`

// startSearchProxy points DEVBOX_SEARCH_HOST at a proxy reading an nxv index
// that holds one package, "hello".
func startSearchProxy(t *testing.T) {
	t.Helper()
	index := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Query().Get("q") == "hello", r.URL.Path == "/api/v1/packages/hello":
			_, _ = w.Write([]byte(helloRows))
		case r.URL.Path == "/api/v1/search":
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(index.Close)

	proxy := httptest.NewServer(searchproxy.NewHandler(nxv.NewClient(index.URL), searchproxy.DefaultSystems))
	t.Cleanup(proxy.Close)
	t.Setenv("DEVBOX_SEARCH_HOST", proxy.URL)
}

func execSearch(t *testing.T, query string) (string, error) {
	t.Helper()
	out := &bytes.Buffer{}
	cmd := searchCmd()
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{query})
	err := cmd.Execute()
	return out.String(), err
}

func runSearch(t *testing.T, query string) string {
	t.Helper()
	out, err := execSearch(t, query)
	require.NoError(t, err)
	return out
}

func TestSearchRendersTable(t *testing.T) {
	startSearchProxy(t)

	out := runSearch(t, "hello")

	assert.Contains(t, out, "hello")
	assert.Contains(t, out, "2.12.1")
	assert.Contains(t, out, "aarch64-darwin")
	assert.Contains(t, out, "x86_64-linux")
}

func TestSearchWithNoMatches(t *testing.T) {
	startSearchProxy(t)

	assert.Contains(t, runSearch(t, "nosuchpackage"), `No results found for "nosuchpackage"`)
}

func TestSearchSurfacesABrokenSearchService(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(broken.Close)
	t.Setenv("DEVBOX_SEARCH_HOST", broken.URL)

	out, err := execSearch(t, "hello")

	require.Error(t, err)
	assert.NotContains(t, out, "No results found")
}

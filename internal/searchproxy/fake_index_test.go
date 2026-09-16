// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package searchproxy_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.jetify.com/devbox/internal/nxv"
	"go.jetify.com/devbox/internal/searchproxy"
)

type pin struct {
	Name         string `json:"name"`
	Constraint   string `json:"constraint"`
	Version      string `json:"version"`
	Attr         string `json:"attr"`
	Rev          string `json:"rev"`
	LastModified string `json:"last_modified"`
}

func (p pin) exact() bool { return p.Constraint == p.Version }

func loadJSON[T any](t *testing.T, path ...string) T {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(append([]string{"testdata"}, path...)...))
	require.NoError(t, err)
	var value T
	require.NoError(t, json.Unmarshal(data, &value))
	return value
}

func corpus(t *testing.T) []pin {
	t.Helper()
	return loadJSON[[]pin](t, "corpus.json")
}

// fakeIndex is an nxv server that answers from testdata/nxv, 404s on anything
// else, and records every path it was asked for.
type fakeIndex struct {
	url   string
	mu    sync.Mutex
	paths []string

	// systems answers /packages/{attr}/systems. Nil makes the endpoint 404,
	// which is how an index without per-system observations behaves.
	systems func(attr string, query url.Values) []nxv.SystemObservation
}

// requestedPaths returns the paths the index was asked for, oldest first.
func (f *fakeIndex) requestedPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.paths)
}

func newFakeIndex(t *testing.T) *fakeIndex {
	t.Helper()
	return newFakeIndexWithHealth(t, loadJSON[json.RawMessage](t, "nxv", "health.json"))
}

func newFakeIndexWithHealth(t *testing.T, health json.RawMessage) *fakeIndex {
	t.Helper()
	versions := loadJSON[map[string]json.RawMessage](t, "nxv", "versions.json")
	packages := loadJSON[map[string]json.RawMessage](t, "nxv", "packages.json")
	searches := loadJSON[map[string]json.RawMessage](t, "nxv", "search.json")
	index := &fakeIndex{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		index.record(r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		body, ok := searches[r.URL.Query().Get("q")]
		if !ok {
			body = json.RawMessage(`{"data":[]}`)
		}
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/api/v1/packages/{attr}/systems", func(w http.ResponseWriter, r *http.Request) {
		index.record(r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		if index.systems == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"not found"}}`))
			return
		}
		rows := index.systems(r.PathValue("attr"), r.URL.Query())
		body, err := json.Marshal(map[string]any{"data": rows})
		require.NoError(t, err)
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		index.record(r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(health)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		index.record(r.URL.RequestURI())
		key := strings.TrimPrefix(r.URL.Path, "/api/v1/packages/")
		body, ok := versions[strings.Replace(key, "/versions/", "/", 1)]
		if !ok {
			body, ok = packages[key]
		}
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"not found"}}`))
			return
		}
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	index.url = srv.URL
	return index
}

func (f *fakeIndex) record(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, path)
}

// newProxy starts a proxy backed by [newFakeIndex] and points Devbox's search
// client at it.
func newProxy(t *testing.T) (string, *fakeIndex) {
	t.Helper()
	index := newFakeIndex(t)
	return startProxy(t, index), index
}

// newProxyWithIndexHead starts a proxy over an index reporting head as the
// nixpkgs commit it has read up to.
func newProxyWithIndexHead(t *testing.T, head string) string {
	t.Helper()
	health, err := json.Marshal(map[string]string{"index_commit": head})
	require.NoError(t, err)
	return startProxy(t, newFakeIndexWithHealth(t, health))
}

func startProxyWithSystems(t *testing.T, index *fakeIndex, systems []string) string {
	t.Helper()
	srv := httptest.NewServer(searchproxy.NewHandler(nxv.NewClient(index.url), systems))
	t.Cleanup(srv.Close)
	return srv.URL
}

func startProxy(t *testing.T, index *fakeIndex) string {
	t.Helper()
	srv := httptest.NewServer(searchproxy.NewHandler(nxv.NewClient(index.url), searchproxy.DefaultSystems))
	t.Cleanup(srv.Close)
	t.Setenv("DEVBOX_SEARCH_HOST", srv.URL)
	return srv.URL
}

// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

// Package searchproxy serves the Devbox search API that DEVBOX_SEARCH_HOST
// points at, answering from an nxv version index.
package searchproxy

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"go.jetify.com/devbox/internal/nxv"
)

// NewHandler returns the Devbox search API backed by index.
// NewHandler serves the Devbox search API from index, reporting only systems.
// A nil or empty systems reports every system nixpkgs lists.
func NewHandler(index *nxv.Client, systems []string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v2/search", func(w http.ResponseWriter, r *http.Request) {
		searchV2(w, r, index)
	})
	mux.HandleFunc("GET /v2/pkg", func(w http.ResponseWriter, r *http.Request) {
		pkgV2(w, r, index, systems)
	})
	mux.HandleFunc("GET /v2/resolve", func(w http.ResponseWriter, r *http.Request) {
		resolveV2(w, r, index, systems)
	})
	mux.HandleFunc("GET /v1/resolve", func(w http.ResponseWriter, r *http.Request) {
		resolveV1(w, r, index, systems)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		slog.Error("marshal response", "err", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// makeMapLike allocates a map with m's own key and value types, which lets a
// caller fill a map whose value type has no name.
func makeMapLike[K comparable, V any](m map[K]V, size int) map[K]V {
	return make(map[K]V, size)
}

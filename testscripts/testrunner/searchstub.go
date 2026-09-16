package testrunner

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// recordSearchEnv makes the stub fetch and save any request it has no recording
// for, instead of failing the test.
const recordSearchEnv = "DEVBOX_TEST_RECORD_SEARCH"

// upstreamSearchHost is recorded from, and only when recording.
const upstreamSearchHost = "https://search.devbox.sh"

// searchStubOnce keeps one stub for the whole test binary. The recordings are
// read-only during a normal run, so there is nothing to isolate per script, and
// a server per script would be thousands of them.
var (
	searchStubOnce sync.Once
	searchStubURL  string
)

// startSearchStub serves the Devbox search API from recordings under
// testdata/search. Call it with the top-level test's T: the stub is shared by
// every script, so binding it to the first script that happened to need it
// would close it while the rest were still using it.
//
// The tests resolve real package versions and then install them from Nix, so
// the recordings hold real resolutions rather than synthetic ones. Serving them
// locally keeps the suite from depending on search.devbox.sh being reachable:
// it was timing out and failing runs that had nothing to do with the change
// under test, which is worse than useless in a gate.
//
// Re-record with:
//
//	DEVBOX_TEST_RECORD_SEARCH=1 devbox run -- go test -count=1 ./testscripts/
func startSearchStub(topLevel testing.TB) {
	searchStubOnce.Do(func() {
		recording := os.Getenv(recordSearchEnv) != ""
		dir := searchRecordingDir()

		server := httptest.NewServer(http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				serveRecording(w, r, dir, recording)
			}))
		searchStubURL = server.URL
		// The stub outlives every script, so it is closed when the binary is,
		// not per test.
		topLevel.Cleanup(server.Close)
	})
}

func serveRecording(w http.ResponseWriter, r *http.Request, dir string, recording bool) {
	path := filepath.Join(dir, recordingName(r.URL))

	if body, err := os.ReadFile(path); err == nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
		return
	}

	if !recording {
		// A 404 reaches devbox as "not found", which reads like a missing
		// package rather than a missing recording, so say which it is.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"error":%q}`, fmt.Sprintf(
			"no search recording for %s; re-record with %s=1",
			r.URL.RequestURI(), recordSearchEnv))
		return
	}

	body, status, err := fetchUpstream(r.URL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// Record misses too: a 404 from the real service is a real answer, and
	// re-fetching it on every run is what this exists to avoid.
	if err := os.MkdirAll(dir, 0o755); err == nil && status < 500 {
		_ = os.WriteFile(path, body, 0o600)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func fetchUpstream(requested *url.URL) ([]byte, int, error) {
	target := upstreamSearchHost + requested.RequestURI()
	resp, err := http.Get(target) //nolint:noctx,gosec
	if err != nil {
		return nil, 0, fmt.Errorf("record %s: %w", target, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("record %s: %w", target, err)
	}
	return body, resp.StatusCode, nil
}

var recordingNameUnsafe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// recordingName maps a request to a file name that names the request, so a
// recording can be matched to the pin it answers by reading the directory.
func recordingName(requested *url.URL) string {
	name := strings.TrimPrefix(requested.Path, "/") + "_" + requested.RawQuery
	name = recordingNameUnsafe.ReplaceAllString(name, "_")
	return strings.Trim(name, "_") + ".json"
}

func searchRecordingDir() string {
	// Resolved from this file's package directory rather than the script's
	// WorkDir, which testscript sets to a temp copy.
	wd, err := os.Getwd()
	if err != nil {
		return filepath.Join("testrunner", "testdata", "search")
	}
	if filepath.Base(wd) == "testrunner" {
		return filepath.Join(wd, "testdata", "search")
	}
	return filepath.Join(wd, "testrunner", "testdata", "search")
}

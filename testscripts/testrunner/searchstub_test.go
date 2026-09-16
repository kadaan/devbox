package testrunner

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordingNameNamesTheRequest(t *testing.T) {
	for _, test := range []struct{ uri, want string }{
		{"/v2/resolve?name=go&version=1.19", "v2_resolve_name_go_version_1.19.json"},
		{"/v1/resolve?name=hello&version=latest", "v1_resolve_name_hello_version_latest.json"},
		{"/v2/resolve?name=stdenv.cc.cc.lib&version=latest", "v2_resolve_name_stdenv.cc.cc.lib_version_latest.json"},
	} {
		parsed, err := url.Parse(test.uri)
		require.NoError(t, err)
		assert.Equal(t, test.want, recordingName(parsed))
	}
}

// A miss must say it is a missing recording. Devbox turns a 404 into "package
// not found", which would send a contributor looking for the wrong problem.
func TestServeRecordingExplainsAMiss(t *testing.T) {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v2/resolve?name=absent&version=1.0", nil)

	serveRecording(recorder, req, t.TempDir(), false)

	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "no search recording")
	assert.Contains(t, recorder.Body.String(), recordSearchEnv)
}

func TestServeRecordingServesARecording(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "v2_resolve_name_go_version_1.19.json"),
		[]byte(`{"name":"go","version":"1.19"}`), 0o600))

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v2/resolve?name=go&version=1.19", nil)

	serveRecording(recorder, req, dir, false)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.JSONEq(t, `{"name":"go","version":"1.19"}`, recorder.Body.String())
}

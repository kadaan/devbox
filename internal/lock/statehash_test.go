package lock

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jetify.com/devbox/internal/nix"
)

func writeDevEnvCache(t *testing.T, projectDir, contents string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(projectDir, ".devbox"), 0o755))
	require.NoError(t, os.WriteFile(printDevEnvCachePath(projectDir), []byte(contents), 0o600))
}

func TestDevEnvHasCollectedStorePathsNoCache(t *testing.T) {
	collected, err := devEnvHasCollectedStorePaths(t.Context(), t.TempDir())
	require.NoError(t, err)
	assert.False(t, collected, "a project with no cached environment cannot have a collected one")
}

func TestDevEnvHasCollectedStorePathsNoStorePaths(t *testing.T) {
	dir := t.TempDir()
	writeDevEnvCache(t, dir, `{"variables":{"HOME":{"type":"exported","value":"/home/user"}}}`)

	collected, err := devEnvHasCollectedStorePaths(t.Context(), dir)
	require.NoError(t, err)
	assert.False(t, collected)
}

// An unresolved-output placeholder is in every real cache on Darwin. It can
// never exist on disk, so if it were treated as collected the environment
// would be recomputed on every command forever.
func TestDevEnvHasCollectedStorePathsIgnoresPlaceholders(t *testing.T) {
	dir := t.TempDir()
	writeDevEnvCache(t, dir, `{"variables":{"NIX_CFLAGS_COMPILE":{"type":"exported",`+
		`"value":"-isystem /nix/store/eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee-libsbuf-14.1.0-dev/include"}}}`)

	collected, err := devEnvHasCollectedStorePaths(t.Context(), dir)
	require.NoError(t, err)
	assert.False(t, collected, "placeholder paths must not be mistaken for collected ones")
}

func TestDevEnvHasCollectedStorePathsPresentPath(t *testing.T) {
	present := anyLocalStorePath(t)

	dir := t.TempDir()
	writeDevEnvCache(t, dir, `{"variables":{"PATH":{"type":"exported","value":"`+present+`/bin"}}}`)

	collected, err := devEnvHasCollectedStorePaths(t.Context(), dir)
	require.NoError(t, err)
	assert.False(t, collected)
}

// The case the whole check exists for: the cache names a path that the store
// no longer has.
func TestDevEnvHasCollectedStorePathsCollectedPath(t *testing.T) {
	requireNix(t)

	dir := t.TempDir()
	// Syntactically valid and vanishingly unlikely to exist.
	gone := "/nix/store/zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz-collected-1.0"
	writeDevEnvCache(t, dir, `{"variables":{"PATH":{"type":"exported","value":"`+gone+`/bin"}}}`)

	collected, err := devEnvHasCollectedStorePaths(t.Context(), dir)
	require.NoError(t, err)
	assert.True(t, collected)
}

func requireNix(t *testing.T) {
	t.Helper()
	if !nix.BinaryInstalled() {
		t.Skip("needs nix to confirm a path is absent from the store")
	}
}

// anyLocalStorePath returns a store path that exists on this machine, so that
// the "nothing was collected" case is exercised against a real path rather
// than a fabricated one.
func anyLocalStorePath(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir("/nix/store")
	if err != nil {
		t.Skip("no /nix/store on this machine")
	}
	for _, e := range entries {
		p := filepath.Join("/nix/store", e.Name())
		if got := nix.StorePaths([]byte(p)); len(got) == 1 && got[0] == p {
			return p
		}
	}
	t.Skip("no usable store path found")
	return ""
}

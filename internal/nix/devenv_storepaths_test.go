package nix

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorePaths(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "no store paths",
			in:   `{"variables":{"HOME":{"type":"exported","value":"/home/user"}}}`,
			want: []string{},
		},
		{
			name: "bare path",
			in:   `/nix/store/0b9ji819djny30jig63jg6zxj2jff9qw-gzip-1.14`,
			want: []string{"/nix/store/0b9ji819djny30jig63jg6zxj2jff9qw-gzip-1.14"},
		},
		{
			name: "trailing subdirectory is not part of the path",
			in:   `/nix/store/0b9ji819djny30jig63jg6zxj2jff9qw-gzip-1.14/bin/gzip`,
			want: []string{"/nix/store/0b9ji819djny30jig63jg6zxj2jff9qw-gzip-1.14"},
		},
		{
			name: "colon separated PATH",
			in:   `/nix/store/0b9ji819djny30jig63jg6zxj2jff9qw-gzip-1.14/bin:/nix/store/3wz2j41nnr1badij6fcnip5vgrfpdd82-findutils-4.10.0/bin`,
			want: []string{
				"/nix/store/0b9ji819djny30jig63jg6zxj2jff9qw-gzip-1.14",
				"/nix/store/3wz2j41nnr1badij6fcnip5vgrfpdd82-findutils-4.10.0",
			},
		},
		{
			name: "duplicates collapse and result is sorted",
			in:   `/nix/store/3wz2j41nnr1badij6fcnip5vgrfpdd82-findutils-4.10.0 /nix/store/0b9ji819djny30jig63jg6zxj2jff9qw-gzip-1.14 /nix/store/3wz2j41nnr1badij6fcnip5vgrfpdd82-findutils-4.10.0`,
			want: []string{
				"/nix/store/0b9ji819djny30jig63jg6zxj2jff9qw-gzip-1.14",
				"/nix/store/3wz2j41nnr1badij6fcnip5vgrfpdd82-findutils-4.10.0",
			},
		},
		{
			// print-dev-env emits these for outputs it has not resolved. They
			// never exist on disk, so treating one as a real path would report
			// the environment as stale on every single command.
			name: "unresolved output placeholder is ignored",
			in:   `-isystem /nix/store/eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee-libsbuf-14.1.0-dev/include`,
			want: []string{},
		},
		{
			// -fmacro-prefix-map=<old>=<new> puts an '=' straight after a store
			// path. Absorbing it yields a path that does not exist, which would
			// also mean permanent staleness.
			name: "equals sign after a path is a delimiter, not part of the name",
			in:   `-fmacro-prefix-map=/nix/store/395mclyn4inz8fip5ziajhbn3x9kp41i-libsbuf-14.1.0-dev=/nix/store/eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee-libsbuf-14.1.0-dev`,
			want: []string{"/nix/store/395mclyn4inz8fip5ziajhbn3x9kp41i-libsbuf-14.1.0-dev"},
		},
		{
			name: "quoted inside json string",
			in:   `{"PATH":{"type":"exported","value":"/nix/store/0b9ji819djny30jig63jg6zxj2jff9qw-gzip-1.14/bin"}}`,
			want: []string{"/nix/store/0b9ji819djny30jig63jg6zxj2jff9qw-gzip-1.14"},
		},
		{
			name: "hash must be exactly 32 base32 characters",
			in:   `/nix/store/tooshort-gzip-1.14 /nix/store/0b9ji819djny30jig63jg6zxj2jff9qw-ok-1.0`,
			want: []string{"/nix/store/0b9ji819djny30jig63jg6zxj2jff9qw-ok-1.0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, StorePaths([]byte(tt.in)))
		})
	}
}

// TestMissingStorePathsAllPresent covers the path that runs on every command.
// It must answer without consulting Nix, so it is driven with real files
// rather than store paths.
func TestMissingStorePathsAllPresent(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for _, name := range []string{"a", "b", "c"} {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
		paths = append(paths, p)
	}

	missing, err := MissingStorePaths(t.Context(), paths)
	require.NoError(t, err)
	assert.Empty(t, missing)
}

func TestMissingStorePathsEmptyInput(t *testing.T) {
	missing, err := MissingStorePaths(t.Context(), nil)
	require.NoError(t, err)
	assert.Empty(t, missing)
}

// TestMissingStorePathsBrokenSymlink guards the use of Lstat over Stat: a
// dangling symlink is present as a directory entry but resolves to nothing,
// and it is not the signal this function looks for. Garbage collection removes
// the entry itself.
func TestMissingStorePathsBrokenSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "dangling")
	require.NoError(t, os.Symlink(filepath.Join(dir, "nonexistent"), link))

	missing, err := MissingStorePaths(t.Context(), []string{link})
	require.NoError(t, err)
	assert.Empty(t, missing, "a dangling symlink still occupies its path")
}

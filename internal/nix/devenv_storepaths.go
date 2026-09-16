package nix

import (
	"context"
	"os"
	"regexp"
	"slices"
)

// storePathRE matches the root of an absolute Nix store path.
//
// The hash class omits e, o, t and u because those are absent from Nix's
// base-32 alphabet. That exclusion is load-bearing rather than cosmetic:
// `nix print-dev-env` emits placeholder paths whose hash is 32 'e's for
// outputs it has not resolved, and those paths never exist on disk. Matching
// one would report the environment as permanently stale.
//
// The name class omits '=' and '?', which Nix does allow in a store name but
// which in practice appear here as delimiters inside compiler flags --
// `-fmacro-prefix-map=<old>=<new>` yields "...-libsbuf-14.1.0-dev=", whose
// trailing '=' is not part of the path. Truncating a genuine name would be the
// worse error, so callers confirm a suspected absence with Nix before acting
// on it; see MissingStorePaths.
var storePathRE = regexp.MustCompile(`/nix/store/[0-9a-df-np-sv-z]{32}-[A-Za-z0-9+._-]+`)

// StorePaths returns the unique Nix store paths referenced anywhere in b,
// sorted. It scans raw bytes so that it finds paths embedded in shell
// fragments and in colon-separated variables like PATH, not just paths that
// occupy a whole JSON value.
func StorePaths(b []byte) []string {
	matches := storePathRE.FindAll(b, -1)
	paths := make([]string, 0, len(matches))
	for _, m := range matches {
		paths = append(paths, string(m))
	}
	slices.Sort(paths)
	return slices.Compact(paths)
}

// MissingStorePaths returns the paths that are absent from the Nix store.
//
// A missing path means the store was garbage collected out from under whatever
// recorded the path. Detection has to be cheap because it gates every command
// that enters a Devbox environment, so the common case -- everything present --
// is answered by a stat per path and spawns no subprocess. Only when a path
// looks absent does this ask Nix, whose answer is authoritative and so cannot
// be fooled by a name this package parsed wrongly.
func MissingStorePaths(ctx context.Context, paths []string) ([]string, error) {
	suspect := []string{}
	for _, p := range paths {
		if _, err := os.Lstat(p); err != nil {
			suspect = append(suspect, p)
		}
	}
	if len(suspect) == 0 {
		return nil, nil
	}

	inStore, err := StorePathsAreInStore(ctx, suspect)
	if err != nil {
		// Nix could not answer, so we cannot distinguish a collected path from
		// a path this package mis-parsed. Report nothing: a false "up to date"
		// leaves the caller no worse off than before this check existed, while
		// a false "stale" would recompute the environment on every command.
		return nil, err
	}

	missing := []string{}
	for _, p := range suspect {
		if !inStore[p] {
			missing = append(missing, p)
		}
	}
	return missing, nil
}

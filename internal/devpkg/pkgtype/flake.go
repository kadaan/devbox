package pkgtype

import (
	"go.jetpack.io/devbox/nix/flake"
	"strings"
)

func IsFlake(s string) bool {
	if IsRunX(s) {
		return false
	}
	parsed, err := flake.ParseRef(s)
	if err != nil {
		return false
	}
	if parsed.Type == flake.TypeIndirect {
		return strings.HasPrefix(parsed.URL, "flake:")
	}
	return true
}

// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package searchproxy

import (
	"cmp"
	"strconv"
	"strings"
)

// CompareVersions orders two version strings the way Nix's own compareVersions
// does, returning -1, 0 or 1. A "pre" component sorts below every other, and a
// missing or non-numeric component below a numeric one.
func CompareVersions(a, b string) int {
	for a != "" || b != "" {
		var componentA, componentB string
		componentA, a = nextComponent(a)
		componentB, b = nextComponent(b)
		if order := compareComponent(componentA, componentB); order != 0 {
			return order
		}
	}
	return 0
}

// nextComponent takes the leading run of digits or of non-separator
// non-digits off version, after dropping the separators before it.
func nextComponent(version string) (component, rest string) {
	version = strings.TrimLeft(version, ".-")
	if version == "" {
		return "", ""
	}
	end := 0
	for end < len(version) && isDigit(version[end]) == isDigit(version[0]) &&
		version[end] != '.' && version[end] != '-' {
		end++
	}
	return version[:end], version[end:]
}

func compareComponent(left, right string) int {
	leftNumber, leftErr := strconv.ParseInt(left, 10, 64)
	rightNumber, rightErr := strconv.ParseInt(right, 10, 64)
	switch {
	case leftErr == nil && rightErr == nil:
		return cmp.Compare(leftNumber, rightNumber)
	case left == "pre" && right != "pre":
		return -1
	case right == "pre" && left != "pre":
		return 1
	case rightErr == nil:
		return -1
	case leftErr == nil:
		return 1
	}
	return strings.Compare(left, right)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// hasVersionPrefix reports whether version's leading components equal those of
// prefix, comparing each componentwise so "3.12" excludes "3.120". An empty
// prefix matches every version.
func hasVersionPrefix(version, prefix string) bool {
	for prefix != "" {
		var have, want string
		have, version = nextComponent(version)
		want, prefix = nextComponent(prefix)
		if compareComponent(have, want) != 0 {
			return false
		}
	}
	return true
}

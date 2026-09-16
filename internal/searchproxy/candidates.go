// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package searchproxy

import (
	"regexp"
	"strings"
)

// familyDepth caps a family suffix at a major (jdk8) or a major and minor
// (go_1_22).
const familyDepth = 2

// AttrCandidates returns the nixpkgs attribute paths to try for a Devbox
// package name and constraint: name itself, then the mechanical family suffixes
// built from the constraint's leading numeric components, most specific first.
func AttrCandidates(name, constraint string) []string {
	candidates := []string{name}
	components := leadingNumbers(constraint)
	for depth := min(familyDepth, len(components)); depth > 0; depth-- {
		prefix := components[:depth]
		joined := strings.Join(prefix, "_")
		candidates = append(candidates,
			name+strings.Join(prefix, ""),
			name+"_"+joined,
			name+"-"+joined+"_x")
	}
	return candidates
}

func leadingNumbers(constraint string) []string {
	var components []string
	rest := constraint
	for {
		digits := 0
		for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
			digits++
		}
		if digits == 0 {
			return components
		}
		components = append(components, rest[:digits])
		rest = rest[digits:]
		if !strings.HasPrefix(rest, ".") {
			return components
		}
		rest = rest[1:]
	}
}

// familySuffix matches the suffixes [AttrCandidates] builds: a bare major or
// major-minor (python314), an underscore-separated one (go_1_22, nodejs_24), or
// the legacy _x form (nodejs-18_x).
var familySuffix = regexp.MustCompile(`^(\d+(\d+)*|_\d+(_\d+)*|-\d+(_\d+)*_x)$`)

// IsFamilyAttr reports whether attr is name itself or name carrying one of the
// mechanical family suffixes [AttrCandidates] builds.
//
// It is the inverse of AttrCandidates, for the one case that cannot enumerate:
// "latest" carries no version to derive a suffix from, so the family members
// have to be recognised among an index search's results instead. Matching on
// attribute-path shape alone is what keeps that from becoming a pname lookup —
// a search hit whose path is not a mechanical family member is never accepted.
func IsFamilyAttr(name, attr string) bool {
	if attr == name {
		return true
	}
	suffix, found := strings.CutPrefix(attr, name)
	if !found || suffix == "" {
		return false
	}
	return familySuffix.MatchString(suffix)
}

// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package searchproxy_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.jetify.com/devbox/internal/searchproxy"
)

// ascending pairs the corpus's non-semver version strings with the version they
// must sort below, each string appearing at least once.
var ascending = [][2]string{
	{"0.08", "0.18"},
	{"0.18", "1.13"},
	{"1.13", "1.14"},
	{"1.14", "1.17"},
	{"1.17", "1.35"},
	{"1.35", "2.40"},
	{"2.40", "3.0"},
	{"3.0", "3.7.0.2"},
	{"3.7.0.2", "4.9"},
	{"4.9", "6.0"},
	{"6.0", "9.5"},
	{"9.5", "9.10"},
	{"9.10", "9.11"},
	{"9.11", "14.4"},
	{"14.4", "76.1"},
	{"76.1", "11076708"},
	{"8u352-ga", "8u362-ga"},
	{"8u362-ga", "8u462-b08"},
	{"17.0.11+9", "17.0.15+6"},
	{"17.0.15+6", "21.0.5+11"},
	{"2025-11-05", "20240122"},
	{"20240122", "20251022"},
	{"20251022", "20260107.1"},
	{"1.0", "1.0.1"},
	{"2.0.0-pre", "2.0.0"},
	{"1.0pre", "1.0"},
}

func TestCompareVersions(t *testing.T) {
	for _, pair := range ascending {
		lower, higher := pair[0], pair[1]
		t.Run(lower+" < "+higher, func(t *testing.T) {
			assert.Equal(t, -1, searchproxy.CompareVersions(lower, higher))
			assert.Equal(t, 1, searchproxy.CompareVersions(higher, lower))
			assert.Equal(t, 0, searchproxy.CompareVersions(lower, lower))
		})
	}
}

func TestCompareVersionsCoversEveryNonSemverCorpusVersion(t *testing.T) {
	covered := map[string]bool{}
	for _, pair := range ascending {
		covered[pair[0]] = true
		covered[pair[1]] = true
	}

	semver := regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	nonSemver := map[string]bool{}
	for _, pinned := range corpus(t) {
		if !semver.MatchString(pinned.Version) {
			nonSemver[pinned.Version] = true
		}
	}

	assert.Len(t, nonSemver, 27)
	for version := range nonSemver {
		assert.True(t, covered[version], version)
	}
}

func TestCompareVersionsRanksTheDateStringAbove(t *testing.T) {
	assert.Equal(t, 1, searchproxy.CompareVersions("20151023-a0ff256", "4.0.2"))
}

// alphaBelowNumeric pairs versions whose order turns on a non-numeric component
// meeting a numeric one, as Nix's own builtins.compareVersions ranks them.
var alphaBelowNumeric = [][2]string{
	{"2.3a", "2.3.1"},
	{"1.0a", "1.0.1"},
	{"8u462", "8.0.462"},
	{"unstable-2024-05-01", "1.2.3"},
}

func TestCompareVersionsRanksANonNumericComponentBelowANumericOne(t *testing.T) {
	for _, pair := range alphaBelowNumeric {
		lower, higher := pair[0], pair[1]
		t.Run(lower+" < "+higher, func(t *testing.T) {
			assert.Equal(t, -1, searchproxy.CompareVersions(lower, higher))
			assert.Equal(t, 1, searchproxy.CompareVersions(higher, lower))
		})
	}
}

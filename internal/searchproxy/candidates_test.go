// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package searchproxy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.jetify.com/devbox/internal/searchproxy"
)

func TestAttrCandidatesFamilySuffixes(t *testing.T) {
	for _, want := range []struct {
		name, version, attr string
	}{
		{"python", "3.12.3", "python312"},
		{"python", "3.9.9", "python3"},
		{"nodejs", "18.19.1", "nodejs_18"},
		{"nodejs", "18.2.0", "nodejs-18_x"},
		{"jdk", "8u362-ga", "jdk8"},
		{"go", "1.22.1", "go_1_22"},
		{"zulu", "8.0.382", "zulu8"},
		{"ruby", "3.2.2", "ruby_3_2"},
	} {
		t.Run(want.attr, func(t *testing.T) {
			candidates := searchproxy.AttrCandidates(want.name, want.version)
			assert.Equal(t, want.name, candidates[0])
			assert.Contains(t, candidates, want.attr)
		})
	}
}

func TestAttrCandidatesWithoutLeadingNumber(t *testing.T) {
	assert.Equal(t, []string{"jq"}, searchproxy.AttrCandidates("jq", "latest"))
}

func TestAttrCandidatesCoverCorpus(t *testing.T) {
	for _, pinned := range corpus(t) {
		key := pinned.Name + "@" + pinned.Constraint
		assert.Contains(t,
			searchproxy.AttrCandidates(pinned.Name, pinned.Constraint), pinned.Attr, key)
	}
}

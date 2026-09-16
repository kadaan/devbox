// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package nxvops_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// stubNxv writes an executable standing in for nxv that records its arguments,
// and returns its path plus a reader for those arguments.
func stubNxv(t *testing.T) (string, func() []string) {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "nxv")
	argv := filepath.Join(dir, "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argv + "\n"
	require.NoError(t, os.WriteFile(binary, []byte(script), 0o755))

	return binary, func() []string {
		data, err := os.ReadFile(argv)
		if os.IsNotExist(err) {
			return nil
		}
		require.NoError(t, err)
		return strings.Fields(string(data))
	}
}

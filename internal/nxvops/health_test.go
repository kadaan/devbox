// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package nxvops_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jetify.com/devbox/internal/nxvops"
)

const maxLag = 24 * time.Hour

// The report fixtures are recordings of `nxv index --report`: healthy.json from
// an incremental run, unhealthy.json from the run that proved --full corrupts a
// bootstrapped index. stale.json is healthy.json with only its lag changed.
func report(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "report", name+".json"))
	require.NoError(t, err)
	return data
}

func TestCheckHealthPassesOnAHealthyReport(t *testing.T) {
	assert.NoError(t, nxvops.CheckHealth(report(t, "healthy"), maxLag))
}

func TestCheckHealthFailsOnAnUnhealthyReport(t *testing.T) {
	err := nxvops.CheckHealth(report(t, "unhealthy"), maxLag)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unhealthy")
}

func TestCheckHealthFailsOnLagBeyondTheThreshold(t *testing.T) {
	err := nxvops.CheckHealth(report(t, "stale"), maxLag)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "head lag")
}

func TestCheckHealthReportsAnomalies(t *testing.T) {
	err := nxvops.CheckHealth(
		[]byte(`{"healthy":false,"head_lag_hours":9,"anomalies":["count floor breached"]}`),
		maxLag)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "count floor breached")
}

// A report whose shape this does not understand must fail to parse rather than
// read as an unhealthy index: the fields defaulting to false and 0 would report
// every index as broken, and the reverse spelling would pass every index.
func TestCheckHealthFailsOnAReportMissingItsFields(t *testing.T) {
	err := nxvops.CheckHealth([]byte(`{"status":"healthy","head_lag":"7h"}`), maxLag)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "healthy")
}

func TestCheckHealthFailsOnAnUnparseableReport(t *testing.T) {
	assert.Error(t, nxvops.CheckHealth([]byte("Index run report\n  status: healthy\n"), maxLag))
}

func TestCheckHealthFileReadsTheReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	require.NoError(t, os.WriteFile(path, report(t, "healthy"), 0o600))

	assert.NoError(t, nxvops.CheckHealthFile(path, maxLag))
}

func TestCheckHealthFileFailsOnAnUnhealthyReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	require.NoError(t, os.WriteFile(path, report(t, "unhealthy"), 0o600))

	assert.Error(t, nxvops.CheckHealthFile(path, maxLag))
}

func TestCheckHealthFileFailsWhenTheReportIsMissing(t *testing.T) {
	err := nxvops.CheckHealthFile(filepath.Join(t.TempDir(), "absent.json"), maxLag)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "read nxv index report")
}

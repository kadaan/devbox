// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package nxvops

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"go.jetify.com/devbox/internal/redact"
)

// indexReport is the JSON `nxv index --report <path>` writes to that path. The
// pointers separate a field nxv did not write from one it wrote as the zero
// value, so a report in a shape this does not understand fails to parse instead
// of reading as an unhealthy index with no lag.
type indexReport struct {
	Healthy      *bool    `json:"healthy"`
	HeadLagHours *float64 `json:"head_lag_hours"`
	Anomalies    []string `json:"anomalies"`
}

// CheckHealth returns an error when the report says the index is unhealthy or
// its head lag is beyond maxLag.
func CheckHealth(report []byte, maxLag time.Duration) error {
	var parsed indexReport
	if err := json.Unmarshal(report, &parsed); err != nil {
		return redact.Errorf("parse nxv index report: %w", redact.Safe(err))
	}
	if parsed.Healthy == nil {
		return redact.Errorf(`nxv index report has no "healthy" field`)
	}
	if parsed.HeadLagHours == nil {
		return redact.Errorf(`nxv index report has no "head_lag_hours" field`)
	}

	if !*parsed.Healthy {
		if len(parsed.Anomalies) > 0 {
			return redact.Errorf("nxv index is unhealthy: %s",
				redact.Safe(strings.Join(parsed.Anomalies, "; ")))
		}
		return redact.Errorf("nxv index is unhealthy")
	}

	lag := time.Duration(*parsed.HeadLagHours * float64(time.Hour))
	if lag > maxLag {
		return redact.Errorf("nxv index head lag %s is beyond %s",
			redact.Safe(lag), redact.Safe(maxLag))
	}
	return nil
}

// CheckHealthFile reads the report `nxv index --report` wrote to path and checks
// it with [CheckHealth].
//
// It reads a report rather than producing one because `nxv index` ingests: a
// health check that ran it would mutate the index it is meant to observe.
func CheckHealthFile(path string, maxLag time.Duration) error {
	report, err := os.ReadFile(path)
	if err != nil {
		return redact.Errorf("read nxv index report: %w", redact.Safe(err))
	}
	return CheckHealth(report, maxLag)
}

// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

// Package nxv is a client for the nxv version index's HTTP API.
package nxv

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.jetify.com/devbox/internal/redact"
)

// ErrNotFound is returned when the index holds no row for the lookup.
var ErrNotFound = errors.New("not found")

// PackageVersion is one (attribute_path, version) range of the nxv index: the
// span of nixpkgs commits over which that attribute path built that version.
type PackageVersion struct {
	ID              int       `json:"id"`
	Name            string    `json:"name"`
	Version         string    `json:"version"`
	AttributePath   string    `json:"attribute_path"`
	FirstCommitHash string    `json:"first_commit_hash"`
	FirstCommitDate time.Time `json:"first_commit_date"`
	LastCommitHash  string    `json:"last_commit_hash"`
	LastCommitDate  time.Time `json:"last_commit_date"`
	Description     string    `json:"description"`
	Homepage        string    `json:"homepage"`
	Platforms       []string  `json:"platforms"`
}

// Client reads an nxv index over HTTP.
type Client struct {
	host string
}

// NewClient returns a client for the nxv server at host, such as
// "http://127.0.0.1:8080".
func NewClient(host string) *Client {
	return &Client{host: strings.TrimSuffix(host, "/")}
}

// Version returns the index range for an exact attribute path and version. It
// returns [ErrNotFound] when the index holds no such row.
func (c *Client) Version(ctx context.Context, attr, version string) (*PackageVersion, error) {
	return get[PackageVersion](ctx, c.host+
		"/api/v1/packages/"+url.PathEscape(attr)+
		"/versions/"+url.PathEscape(version))
}

// Search returns one row per attribute path matching query, in the order nxv
// ranks them.
func (c *Client) Search(ctx context.Context, query string) ([]PackageVersion, error) {
	rows, err := get[[]PackageVersion](ctx, c.host+
		"/api/v1/search?q="+url.QueryEscape(query))
	if err != nil {
		return nil, err
	}
	return *rows, nil
}

// Versions returns every index range for an attribute path, in the order nxv
// returns them. It returns [ErrNotFound] when the index holds no such path.
func (c *Client) Versions(ctx context.Context, attr string) ([]PackageVersion, error) {
	rows, err := get[[]PackageVersion](ctx, c.host+"/api/v1/packages/"+url.PathEscape(attr))
	if err != nil {
		return nil, err
	}
	return *rows, nil
}

// IndexCommit returns the nixpkgs commit the index has read up to. It is empty
// when the index has never been written.
func (c *Client) IndexCommit(ctx context.Context) (string, error) {
	health, err := getPlain[struct {
		IndexCommit string `json:"index_commit"`
	}](ctx, c.host+"/api/v1/health")
	if err != nil {
		return "", err
	}
	return health.IndexCommit, nil
}

func get[T any](ctx context.Context, endpoint string) (*T, error) {
	// nxv wraps every successful payload in a data envelope.
	envelope, err := getPlain[struct {
		Data T `json:"data"`
	}](ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return &envelope.Data, nil
}

func getPlain[T any](ctx context.Context, endpoint string) (*T, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, redact.Errorf("nxv GET %s: %w", redact.Safe(endpoint), redact.Safe(err))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, redact.Errorf("nxv GET %s: %w", redact.Safe(endpoint), redact.Safe(err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, redact.Errorf("nxv GET %s: read response body: %w",
			redact.Safe(endpoint), redact.Safe(err))
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode >= 400 {
		return nil, redact.Errorf("nxv GET %s: unexpected status code %s: %s",
			redact.Safe(endpoint), redact.Safe(resp.Status), redact.Safe(body))
	}

	var value T
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, redact.Errorf("nxv GET %s: unmarshal response JSON: %w",
			redact.Safe(endpoint), redact.Safe(err))
	}
	return &value, nil
}

// SystemObservation is what one attribute path evaluated to on one system at
// one nixpkgs commit, from nxv's /packages/{attr}/systems endpoint.
//
// A [PackageVersion] row carries the version of whichever system the channel
// snapshot was evaluated for; this carries the version of the system asked
// about. For an attribute path that nixpkgs defines differently per platform
// they disagree, and the observation is the one that describes what will
// actually be installed.
type SystemObservation struct {
	AttributePath string    `json:"attribute_path"`
	System        string    `json:"system"`
	CommitHash    string    `json:"commit_hash"`
	CommitDate    time.Time `json:"commit_date"`
	// Version is empty when the attribute path does not evaluate on this
	// system at this commit.
	Version    string    `json:"version"`
	StoreName  string    `json:"store_name"`
	StorePath  string    `json:"store_path"`
	Outputs    []Output  `json:"outputs"`
	ObservedAt time.Time `json:"observed_at"`
}

// Available reports whether the attribute path can be installed on this system.
//
// A version is not enough: an attribute can exist in a system's package set,
// carry a version, and still have no output path there, because nixpkgs rejects
// it for that platform when its output is forced. The store path is what an
// install needs and what survives both shapes.
func (o SystemObservation) Available() bool {
	return o.Version != "" && o.StorePath != ""
}

// Output is one nix output of an evaluated attribute path.
type Output struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Default reports whether nix installs this output by default.
	Default bool `json:"default"`
}

// SystemsAtCommit returns what each of systems evaluated attr to at commit.
//
// The index evaluates any (system, commit) pair it has not seen before, so the
// first call for a commit is seconds and every later one is a lookup.
func (c *Client) SystemsAtCommit(ctx context.Context, attr, commit string, systems []string) (
	[]SystemObservation, error,
) {
	query := url.Values{"commit": {commit}}
	if len(systems) > 0 {
		query.Set("system", strings.Join(systems, ","))
	}
	return c.systems(ctx, attr, query)
}

// SystemsForVersion returns the observations on system whose version is exactly
// version, which is how a pin only that system ever saw gets resolved.
func (c *Client) SystemsForVersion(ctx context.Context, attr, system, version string) (
	[]SystemObservation, error,
) {
	return c.systems(ctx, attr, url.Values{
		"system":  {system},
		"version": {version},
	})
}

func (c *Client) systems(ctx context.Context, attr string, query url.Values) (
	[]SystemObservation, error,
) {
	rows, err := get[[]SystemObservation](ctx, c.host+
		"/api/v1/packages/"+url.PathEscape(attr)+
		"/systems?"+query.Encode())
	if err != nil {
		return nil, err
	}
	return *rows, nil
}

// EvalSystems reports whether the index will evaluate a (system, commit) pair
// it has not observed yet.
//
// An index that will not can still answer from what it has recorded, so this
// is the difference between "no per-system answer yet" and "no per-system
// answer, ever" — which a caller must know before treating an empty result as
// the truth about a package.
func (c *Client) EvalSystems(ctx context.Context) (bool, error) {
	health, err := getPlain[struct {
		EvalSystems bool `json:"eval_systems"`
	}](ctx, c.host+"/api/v1/health")
	if err != nil {
		return false, err
	}
	return health.EvalSystems, nil
}

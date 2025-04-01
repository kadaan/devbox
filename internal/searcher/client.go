// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package searcher

import (
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"

	"github.com/pkg/errors"
	"go.jetify.com/devbox/internal/build"
	"go.jetify.com/devbox/internal/envir"
	"go.jetify.com/devbox/internal/redact"
)

const searchAPIEndpoint = "https://search.devbox.sh"

var ErrNotFound = errors.New("Not found")

type client struct {
	host string
}

func Client() *client {
	return &client{
		host: envir.GetValueOrDefault(envir.DevboxSearchHost, searchAPIEndpoint),
	}
}

func (c *client) Search(ctx context.Context, query string) (*SearchResults, error) {
	if query == "" {
		return nil, fmt.Errorf("query should not be empty")
	}

	endpoint, err := url.JoinPath(c.host, "v2/search")
	if err != nil {
		return nil, errors.WithStack(err)
	}
	searchURL := endpoint + "?q=" + url.QueryEscape(query)

	searchResults, err := execGet[SearchResultsV2](ctx, searchURL)
	if err != nil {
		return nil, err
	}

	if searchResults == nil || searchResults.TotalResults == 0 {
		return nil, ErrNotFound
	}

	results := &SearchResults{}
	for _, result := range searchResults.Results {
		endpoint, err = url.JoinPath(c.host, "v2/pkg")
		if err != nil {
			return nil, errors.WithStack(err)
		}
		resolveURL := endpoint + "?name=" + url.QueryEscape(result.Name)

		packageInfo, resolveErr := execGet[PackageV2](ctx, resolveURL)
		if resolveErr != nil {
			return nil, resolveErr
		}

		if packageInfo == nil {
			return nil, ErrNotFound
		}

		results.NumResults++
		versions := make([]PackageVersion, len(packageInfo.Releases))
		for i, release := range packageInfo.Releases {
			systems := make(map[string]PackageInfo, len(release.Platforms))
			for _, platform := range release.Platforms {
				arch := strings.ReplaceAll(platform.Arch, "-", "_")
				if arch == "arm64" {
					arch = "aarch64"
				}
				os := cases.Lower(language.English).String(platform.OS)
				if os == "macos" {
					os = "darwin"
				}
				system := arch + "-" + os
				systems[system] = PackageInfo{
					System: system,
				}
			}
			versions[i] = PackageVersion{
				PackageInfo: PackageInfo{
					Version: release.Version,
				},
				Systems: systems,
			}
		}
		results.Packages = append(results.Packages, Package{
			Name:        packageInfo.Name,
			NumVersions: len(packageInfo.Releases),
			Versions:    versions,
		})
	}

	return results, nil
}

// Resolve calls the /resolve endpoint of the search service. This returns
// the latest version of the package that matches the version constraint.
func (c *client) Resolve(name, version string) (*PackageVersion, error) {
	if name == "" || version == "" {
		return nil, fmt.Errorf("name and version should not be empty")
	}

	endpoint, err := url.JoinPath(c.host, "v1/resolve")
	if err != nil {
		return nil, errors.WithStack(err)
	}
	searchURL := endpoint +
		"?name=" + url.QueryEscape(name) +
		"&version=" + url.QueryEscape(version)

	return execGet[PackageVersion](context.TODO(), searchURL)
}

// Resolve calls the /resolve endpoint of the search service. This returns
// the latest version of the package that matches the version constraint.
func (c *client) ResolveV2(ctx context.Context, name, version string) (*ResolveResponse, error) {
	if name == "" {
		return nil, redact.Errorf("name is empty")
	}
	if version == "" {
		return nil, redact.Errorf("version is empty")
	}

	endpoint, err := url.JoinPath(c.host, "v2/resolve")
	if err != nil {
		return nil, redact.Errorf("invalid search endpoint host %q: %w", redact.Safe(c.host), redact.Safe(err))
	}
	searchURL := endpoint +
		"?name=" + url.QueryEscape(name) +
		"&version=" + url.QueryEscape(version)

	return execGet[ResolveResponse](ctx, searchURL)
}

var userAgent = fmt.Sprintf("Devbox/%s (%s; %s)", build.Version, runtime.GOOS, runtime.GOARCH)

func execGet[T any](ctx context.Context, url string) (*T, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, redact.Errorf("GET %s: %w", redact.Safe(url), redact.Safe(err))
	}
	req.Header.Set("User-Agent", userAgent)

	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, redact.Errorf("GET %s: %w", redact.Safe(url), redact.Safe(err))
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, redact.Errorf("GET %s: read respoonse body: %w", redact.Safe(url), redact.Safe(err))
	}
	if response.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if response.StatusCode >= 400 {
		return nil, redact.Errorf("GET %s: unexpected status code %s: %s",
			redact.Safe(url),
			redact.Safe(response.Status),
			redact.Safe(data),
		)
	}
	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, redact.Errorf("GET %s: unmarshal response JSON: %w", redact.Safe(url), redact.Safe(err))
	}
	return &result, nil
}

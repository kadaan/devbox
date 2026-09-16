// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package devpkg

import (
	"slices"
	"strings"

	"go.jetify.com/devbox/internal/lock"
	"go.jetify.com/devbox/internal/nix"
)

const legacyPackagesPrefix = "legacyPackages."

// storePathSystems are the systems store paths are filled for, on top of the
// ones a lockfile entry already names.
var storePathSystems = []string{
	"aarch64-darwin",
	"aarch64-linux",
	"x86_64-darwin",
	"x86_64-linux",
}

// InstallablesForSystem returns the package's Nix installables with attribute
// paths qualified for system, delegating to [Package.Installables] for the host.
func (p *Package) InstallablesForSystem(system string) ([]string, error) {
	if system == nix.System() {
		return p.Installables()
	}
	installable, err := p.FlakeInstallable()
	if err != nil {
		return nil, err
	}
	if installable.AttrPath != "" {
		installable.AttrPath = qualifyAttrPath(system, systemRelativeAttrPath(installable.AttrPath))
	}
	return []string{installable.String()}, nil
}

// SystemsMissingStorePaths returns the sorted systems whose store paths the
// lockfile does not hold yet.
func (p *Package) SystemsMissingStorePaths() ([]string, error) {
	entry, err := p.lockfile.Resolve(p.LockfileKey())
	if err != nil {
		return nil, err
	}
	hostStorePaths, err := p.GetResolvedStorePaths()
	if err != nil {
		return nil, err
	}

	missing := []string{}
	if len(hostStorePaths) == 0 {
		missing = append(missing, nix.System())
	}
	for _, system := range candidateSystems(entry.Systems) {
		if system == nix.System() {
			continue
		}
		if info := entry.Systems[system]; info == nil || len(info.Outputs) == 0 {
			missing = append(missing, system)
		}
	}
	slices.Sort(missing)
	return missing, nil
}

func candidateSystems(locked map[string]*lock.SystemInfo) []string {
	systems := slices.Clone(storePathSystems)
	for system := range locked {
		if !slices.Contains(systems, system) {
			systems = append(systems, system)
		}
	}
	return systems
}

// systemRelativeAttrPath strips a leading "legacyPackages.<system>." prefix so
// the path can be re-qualified for another system.
func systemRelativeAttrPath(attrPath string) string {
	if !strings.HasPrefix(attrPath, legacyPackagesPrefix) {
		return attrPath
	}
	parts := strings.SplitN(attrPath, ".", 3)
	if len(parts) < 3 {
		return attrPath
	}
	return parts[2]
}

func qualifyAttrPath(system, attrPath string) string {
	return legacyPackagesPrefix + system + "." + attrPath
}

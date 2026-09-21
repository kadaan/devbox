package shellgen

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/trace"
	"slices"
	"strings"

	"go.jetify.com/devbox/internal/build"
	"go.jetify.com/devbox/internal/devpkg"
	"go.jetify.com/devbox/internal/nix"
	"go.jetify.com/devbox/internal/patchpkg"
	"go.jetify.com/devbox/nix/flake"
)

// flakePlan contains the data to populate the top level flake.nix file
// that builds the devbox environment
type flakePlan struct {
	Stdenv      flake.Ref
	Packages    []*devpkg.Package
	FlakeInputs []flakeInput
	System      string
}

func newFlakePlan(ctx context.Context, devbox devboxer) (*flakePlan, error) {
	ctx, task := trace.NewTask(ctx, "devboxFlakePlan")
	defer task.End()

	for _, pluginConfig := range devbox.Config().IncludedPluginConfigs() {
		if err := devbox.PluginManager().CreateFilesForConfig(pluginConfig); err != nil {
			return nil, err
		}
	}

	packages := devbox.InstallablePackages()

	// Fill the NarInfo Cache concurrently as a perf-optimization, prior to invoking
	// IsInBinaryCache in flakeInputs() and in the flake.nix template.
	if err := devpkg.FillNarInfoCache(ctx, packages...); err != nil {
		return nil, err
	}

	return &flakePlan{
		FlakeInputs: flakeInputs(ctx, packages),
		Stdenv:      devbox.Lockfile().Stdenv(),
		Packages:    packages,
		System:      nix.System(),
	}, nil
}

func (f *flakePlan) needsGlibcPatch() bool {
	for _, in := range f.FlakeInputs {
		if in.Ref == glibcPatchFlakeRef {
			return true
		}
	}
	return false
}

type glibcPatchFlake struct {
	// DevboxFlake provides the devbox binary that will act as the patch
	// flake's builder: the local Devbox source when this machine built the
	// binary, otherwise "github:kadaan/devbox/" + [build.Commit]. See
	// [devboxPatchFlakeRef].
	DevboxFlake flake.Ref

	// NixpkgsGlibcFlakeRef is a flake reference to the nixpkgs flake
	// containing the new glibc package.
	NixpkgsGlibcFlakeRef string

	// Inputs is the attribute set of flake inputs. The key is the input
	// name and the value is a flake reference.
	Inputs map[string]string

	// Outputs is the attribute set of flake outputs. It follows the
	// standard flake output schema of system.name = derivation. The
	// derivation can be any valid Nix expression.
	Outputs struct {
		Packages map[string]map[string]string
	}

	// Dependencies is set of extra packages that are dependencies of the
	// patched packages. For example, a patched Python interpreter might
	// need CUDA packages, but the CUDA packages themselves don't need
	// patching.
	Dependencies []string
}

// devboxPatchFlakeRef returns the flake providing the devbox binary that acts
// as the patch flake's builder.
//
// Never [build.Version]: that is `git describe` output, and
// "0.18.0-ens001-1-g597c2f6f" or anything "-dirty" is not a git ref. GitHub
// falls back to reading it as a SHA and answers 422, so the whole shell fails
// for any project holding a package that needs patching. A released tag is the
// one Version that happens to resolve, which is why this only breaks builds
// made outside a release.
//
// The local source tree wins whenever it resolves, so a build made on this
// machine patches with the code it was built from and needs neither a push nor
// the network. runtime.Caller records the compiling machine's path, so a binary
// built elsewhere finds nothing there and falls through to the commit it was
// built at — which, unlike the version, is always a valid ref.
func devboxPatchFlakeRef(commit string, sourceDir func() (string, error)) flake.Ref {
	src, err := sourceDir()
	if err == nil {
		return flake.Ref{Type: flake.TypePath, Path: src}
	}
	slog.Debug("no local devbox source to patch with; using the commit this binary was built at",
		"commit", commit, "err", err)

	ref := flake.Ref{Type: flake.TypeGitHub, Owner: "kadaan", Repo: "devbox"}
	if isCommitHash(commit) {
		ref.Ref = commit
		return ref
	}

	// An unstamped build (build.Commit is "none") has nothing to pin to. The
	// default branch is a guess, but it is a resolvable one, where a made-up
	// ref is a guaranteed failure.
	slog.Warn("this build records no commit to patch with; using the default branch",
		"commit", commit)
	return ref
}

// isCommitHash reports whether s is a full git object name, which is the only
// form of [build.Commit] that GitHub can resolve.
func isCommitHash(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func newGlibcPatchFlake(nixpkgs flake.Ref, packages []*devpkg.Package) (glibcPatchFlake, error) {
	patchFlake := glibcPatchFlake{
		DevboxFlake:          devboxPatchFlakeRef(build.Commit, build.SourceDir),
		NixpkgsGlibcFlakeRef: nixpkgs.String(),
	}

	for _, pkg := range packages {
		// Check to see if this is a CUDA package. If so, we need to add
		// it to the flake dependencies so that we can patch other
		// packages to reference it (like Python).
		relAttrPath, err := patchFlake.systemRelativeAttrPath(pkg)
		if err != nil {
			return glibcPatchFlake{}, err
		}
		if strings.HasPrefix(relAttrPath, "cudaPackages") {
			if err := patchFlake.addDependency(pkg); err != nil {
				return glibcPatchFlake{}, err
			}
		}

		if !pkg.Patch {
			continue
		}
		if err := patchFlake.addOutput(pkg); err != nil {
			return glibcPatchFlake{}, err
		}
	}

	slog.Debug("creating new patch flake", "flake", &patchFlake)
	return patchFlake, nil
}

// addInput adds a flake input that provides pkg.
func (g *glibcPatchFlake) addInput(pkg *devpkg.Package) error {
	if g.Inputs == nil {
		g.Inputs = make(map[string]string)
	}
	installable, err := pkg.FlakeInstallable()
	if err != nil {
		return err
	}
	inputName := pkg.FlakeInputName()
	g.Inputs[inputName] = installable.Ref.String()
	return nil
}

// addOutput adds a flake output that provides the patched version of pkg.
func (g *glibcPatchFlake) addOutput(pkg *devpkg.Package) error {
	if err := g.addInput(pkg); err != nil {
		return err
	}

	relAttrPath, err := g.systemRelativeAttrPath(pkg)
	if err != nil {
		return err
	}
	if g.Outputs.Packages == nil {
		g.Outputs.Packages = map[string]map[string]string{nix.System(): {}}
	}
	if cached, err := pkg.IsInBinaryCache(); err == nil && cached {
		if expr, err := g.fetchClosureExpr(pkg); err == nil {
			g.Outputs.Packages[nix.System()][relAttrPath] = expr
			return nil
		}
	}

	inputAttrPath, err := g.inputRelativeAttrPath(pkg)
	if err != nil {
		return err
	}
	g.Outputs.Packages[nix.System()][relAttrPath] = inputAttrPath
	return nil
}

// addDependency adds pkg to the derivation's patchDependencies attribute,
// making it available at patch build-time.
func (g *glibcPatchFlake) addDependency(pkg *devpkg.Package) error {
	if err := g.addInput(pkg); err != nil {
		return err
	}
	inputAttrPath, err := g.inputRelativeAttrPath(pkg)
	if err != nil {
		return err
	}

	installable, err := pkg.FlakeInstallable()
	if err != nil {
		return err
	}
	switch installable.Outputs {
	case flake.DefaultOutputs:
		expr := "selectDefaultOutputs " + inputAttrPath
		g.Dependencies = append(g.Dependencies, expr)
	case flake.AllOutputs:
		expr := "selectAllOutputs " + inputAttrPath
		g.Dependencies = append(g.Dependencies, expr)
	default:
		expr := fmt.Sprintf("selectOutputs %s %q", inputAttrPath, installable.SplitOutputs())
		g.Dependencies = append(g.Dependencies, expr)
	}
	return nil
}

// systemRelativeAttrPath strips any leading "legacyPackages.<system>" prefix
// from a package's attribute path.
func (g *glibcPatchFlake) systemRelativeAttrPath(pkg *devpkg.Package) (string, error) {
	installable, err := pkg.FlakeInstallable()
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(installable.AttrPath, "legacyPackages") {
		// Remove the legacyPackages.<system> prefix.
		return strings.SplitN(installable.AttrPath, ".", 3)[2], nil
	}
	return installable.AttrPath, nil
}

// inputRelativeAttrPath joins the package's corresponding flake input with its
// attribute path.
func (g *glibcPatchFlake) inputRelativeAttrPath(pkg *devpkg.Package) (string, error) {
	relAttrPath, err := g.systemRelativeAttrPath(pkg)
	if err != nil {
		return "", err
	}
	atrrPath := strings.Join([]string{"pkgs", pkg.FlakeInputName(), nix.System(), relAttrPath}, ".")
	return atrrPath, nil
}

// TODO: this only handles the first store path, but we should handle all of them
func (g *glibcPatchFlake) fetchClosureExpr(pkg *devpkg.Package) (string, error) {
	storePaths, err := pkg.InputAddressedPaths()
	if err != nil {
		return "", err
	}
	if len(storePaths) == 0 {
		return "", fmt.Errorf("no store path for package %s", pkg.Raw)
	}
	return fmt.Sprintf(`builtins.fetchClosure {
  fromStore = "%s";
  fromPath = "%s";
  inputAddressed = true;
}`, "devpkg.BinaryCache", storePaths[0]), nil
}

// copySystemCUDALib searches for the system's libcuda.so shared library and
// copies it in the flake's lib directory.
func (g *glibcPatchFlake) copySystemCUDALib(flakeDir string) error {
	slog.Debug("found CUDA package in devbox environment, attempting to find system driver libraries")

	searchPath := slices.Concat(
		patchpkg.EnvLDLibrarySearchPath,
		patchpkg.EnvLibrarySearchPath,
		patchpkg.SystemLibSearchPaths,
		patchpkg.CUDALibSearchPaths,
	)
	for lib := range patchpkg.FindSharedLibrary("libcuda.so", searchPath...) {
		logger := slog.With("lib", lib)
		logger.Debug("found potential system CUDA library")

		stat, err := lib.Stat()
		if err != nil {
			logger.Error("skipping system CUDA library because of stat error", "err", err)
		}
		const mib = 1 << 20
		if stat.Size() < 1*mib {
			logger.Debug("skipping system CUDA library because it looks like a stub (size < 1 MiB)", "size", stat.Size())
			continue
		}
		if lib.Soname == "" {
			logger.Debug("skipping system CUDA library because it's missing a soname")
			continue
		}

		libDir := filepath.Join(flakeDir, "lib")
		if err := lib.CopyAndLink(libDir); err == nil {
			slog.Debug("copied system CUDA library to flake directory", "dst", libDir)
		} else {
			slog.Error("can't copy system CUDA library to flake directory", "err", err)
		}
		return err
	}
	return fmt.Errorf("can't find the system CUDA library")
}

func (g *glibcPatchFlake) writeTo(dir string) error {
	wantCUDA := slices.ContainsFunc(g.Dependencies, func(dep string) bool {
		return strings.Contains(dep, "cudaPackages")
	})
	if wantCUDA {
		err := g.copySystemCUDALib(dir)
		if err != nil {
			slog.Debug("error copying system libcuda.so to flake", "dir", dir)
		}
	}
	changed, err := writeFromTemplate(dir, g, "glibc-patch.nix", "flake.nix")
	if err != nil {
		return err
	}
	if changed {
		_ = os.Remove(filepath.Join(dir, "flake.lock"))
	}
	return nil
}

func (g *glibcPatchFlake) LogValue() slog.Value {
	inputs := make([]slog.Attr, 0, 2+len(g.Inputs))
	inputs = append(inputs,
		slog.String("devbox", g.DevboxFlake.String()),
		slog.String("nixpkgs-glibc", g.NixpkgsGlibcFlakeRef),
	)
	for k, v := range g.Inputs {
		inputs = append(inputs, slog.String(k, v))
	}

	var outputs []string
	for _, pkg := range g.Outputs.Packages {
		for attrPath := range pkg {
			outputs = append(outputs, attrPath)
		}
	}
	return slog.GroupValue(
		slog.Attr{Key: "inputs", Value: slog.GroupValue(inputs...)},
		slog.Attr{Key: "outputs", Value: slog.AnyValue(outputs)},
	)
}

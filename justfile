default:
  @just --list

release ONE_PASSWORD_ACCOUNT: (checksum ONE_PASSWORD_ACCOUNT)
  cat $DEVBOX_PROJECT_ROOT/dist/homebrew/SHA256SUMS

checksum ONE_PASSWORD_ACCOUNT: (create-dmg ONE_PASSWORD_ACCOUNT)
  mkdir $DEVBOX_PROJECT_ROOT/dist/homebrew
  cp $DEVBOX_PROJECT_ROOT/dist/darwin/devbox-darwin.dmg $DEVBOX_PROJECT_ROOT/dist/homebrew/devbox-darwin.dmg
  cp $DEVBOX_PROJECT_ROOT/dist/linux/amd64/devbox $DEVBOX_PROJECT_ROOT/dist/homebrew/devbox-linux-amd64
  cp $DEVBOX_PROJECT_ROOT/dist/linux/arm64/devbox $DEVBOX_PROJECT_ROOT/dist/homebrew/devbox-linux-arm64
  shasum -a 256 $DEVBOX_PROJECT_ROOT/dist/homebrew/devbox-darwin.dmg $DEVBOX_PROJECT_ROOT/dist/homebrew/devbox-linux-amd64 $DEVBOX_PROJECT_ROOT/dist/homebrew/devbox-linux-arm64 > $DEVBOX_PROJECT_ROOT/dist/homebrew/SHA256SUMS

create-dmg $ONE_PASSWORD_ACCOUNT: build
  ./bin/create-dmg $DEVBOX_PROJECT_ROOT/dist/darwin/devbox $DEVBOX_PROJECT_ROOT/dist/darwin/devbox

clean:
  @echo "==> Cleanup"
  @rm -rf $DEVBOX_PROJECT_ROOT/dist
  @rm -rf $DEVBOX_PROJECT_ROOT/target

build: clean build-linux build-darwin tarball

build-linux: (build-os "linux" "osusergo netgo static_build" "linux-musl")

build-darwin: (build-os "darwin" "osusergo netgo" "macos-none")
  @echo "==> Making universal darwin binary"
  lipo $DEVBOX_PROJECT_ROOT/dist/darwin/amd64/devbox $DEVBOX_PROJECT_ROOT/dist/darwin/arm64/devbox -create -output $DEVBOX_PROJECT_ROOT/dist/darwin/devbox
  rm -rf $DEVBOX_PROJECT_ROOT/dist/darwin/amd64
  rm -rf $DEVBOX_PROJECT_ROOT/dist/darwin/arm64

build-os os tags type: (build-arch os "amd64" "x86_64-" + type tags) (build-arch os "arm64" "aarch64-" + type tags)

build-arch os arch target tags:
  @echo "==> Building {{os}}/{{arch}}"
  GOOS={{os}} GOARCH={{arch}} CC="zig cc -target {{target}}" CXX="zig c++ -target {{target}}" go build \
      -o $DEVBOX_PROJECT_ROOT/dist/{{os}}/{{arch}}/devbox \
      -a \
      -ldflags "-s -w -X go.jetify.com/devbox/internal/build.Version=$(git describe --tags --candidates=1 --dirty) -X go.jetify.com/devbox/internal/build.Commit=$(git rev-parse HEAD) -X go.jetify.com/devbox/internal/build.CommitDate=$(date -u -d "@$(git show -s --format="%ct" HEAD)" +"%Y-%m-%dT%H:%M:%SZ")" \
      -tags '{{tags}}' \
      -installsuffix netgo \
      ./cmd/devbox

tarball:
  @echo "==> Making tarball"
  tar -czf $DEVBOX_PROJECT_ROOT/dist/linux/devbox.tar.gz -C $DEVBOX_PROJECT_ROOT/dist/linux/amd64 devbox

binary_name := "devbox"
build_flags := ''

release ONE_PASSWORD_ACCOUNT: (checksum ONE_PASSWORD_ACCOUNT)
  cat dist/SHA256SUMS

clean: 
  rm -rf ./dist
  rm -rf ./target

build-linux: clean (_build-linux "amd64") (_build-linux "arm64")

build-darwin: clean (_build-darwin "arm64") (_build-darwin "amd64") _install-makefat
  makefat dist/{{binary_name}}-darwin dist/{{binary_name}}-darwin-amd64 dist/{{binary_name}}-darwin-arm64
  rm dist/{{binary_name}}-darwin-amd64 dist/{{binary_name}}-darwin-arm64

build: build-linux build-darwin

create-dmg $ONE_PASSWORD_ACCOUNT: build
  ./bin/create-dmg dist/{{binary_name}}-darwin devbox dist/{{binary_name}}-darwin.dmg
  rm dist/{{binary_name}}-darwin

checksum ONE_PASSWORD_ACCOUNT: (create-dmg ONE_PASSWORD_ACCOUNT)
  shasum -a 256 dist/* > dist/SHA256SUMS

_install-makefat:
  go install -v github.com/randall77/makefat@latest

_build-darwin arch:
  @ CGO_ENABLED=0 GO111MODULE=on GOOS=darwin GOARCH={{arch}} go build \
      -ldflags="-X go.jetpack.io/devbox/internal/build.Version=$(git describe --tags --candidates=1 --dirty) -X go.jetpack.io/devbox/internal/build.Commit=$(git rev-parse HEAD) -X go.jetpack.io/devbox/internal/build.CommitDate=$(date -u -d "@$(git show -s --format="%ct" HEAD)" +"%Y-%m-%dT%H:%M:%SZ") -s -w" \
      -trimpath \
      -o dist/{{binary_name}}-darwin-{{arch}} \
      ./cmd/devbox

_build-linux arch:
  @ CGO_ENABLED=0 GO111MODULE=on GOOS=linux GOARCH={{arch}} go build \
      -ldflags="-X go.jetpack.io/devbox/internal/build.Version=$(git describe --tags --candidates=1 --dirty) -X go.jetpack.io/devbox/internal/build.Commit=$(git rev-parse HEAD) -X go.jetpack.io/devbox/internal/build.CommitDate=$(date -u -d "@$(git show -s --format="%ct" HEAD)" +"%Y-%m-%dT%H:%M:%SZ") -d -s -w" \
      -trimpath \
      -o dist/{{binary_name}}-linux-{{arch}} \
      ./cmd/devbox

VERSION=$(shell git describe --tags --candidates=1 --dirty)
COMMIT=$(shell git rev-parse HEAD)
COMMIT_DATE_UNIX=$(shell git show -s --format="%ct" HEAD)
COMMIT_DATE=$(shell date -u -d "@$(COMMIT_DATE_UNIX)" +"%Y-%m-%dT%H:%M:%SZ")
BUILD_FLAGS=-ldflags="-X go.jetpack.io/devbox/internal/build.Version=$(VERSION) -X go.jetpack.io/devbox/internal/build.Commit=$(COMMIT) -X go.jetpack.io/devbox/internal/build.CommitDate=$(COMMIT_DATE) -s -w" -trimpath
CERT_ID=$(shell op get item --account ensighten --fields "Developer ID Application Certificate Name" o4d7yvksmnb4lbhwm5ntmjr4se)
SRC=$(shell find ./cmd/devbox -name '*.go')
INSTALL_DIR ?= ~/bin
.PHONY: binaries clean release install

devbox: $(SRC)
	go build $(BUILD_FLAGS) .

install: devbox
	mkdir -p $(INSTALL_DIR)
	rm -f $(INSTALL_DIR)/aws-okta
	cp -a ./aws-okta $(INSTALL_DIR)
	codesign --options runtime --timestamp --sign "$(CERT_ID)" $(INSTALL_DIR)/aws-okta || true

binaries: devbox-darwin

clean:
	rm -rf ./dist

release: clean binaries devbox-darwin.dmg SHA256SUMS
	@echo -e "\nTo update homebrew-cask run\n\n    cask-repair -v $(shell echo $(VERSION) | sed 's/v\(.*\)/\1/') devbox\n$(cat dist/SHA256SUMS)\n"

devbox-darwin-amd64: $(SRC)
	CGO_ENABLED=0 GO111MODULE=on GOOS=darwin GOARCH=amd64 go build $(BUILD_FLAGS) -o dist/$@ ./cmd/devbox

devbox-darwin-arm64: $(SRC)
	CGO_ENABLED=0 GO111MODULE=on GOOS=darwin GOARCH=arm64 go build $(BUILD_FLAGS) -o dist/$@ ./cmd/devbox

install-makefat:
	go install -v github.com/randall77/makefat@latest

devbox-darwin: install-makefat devbox-darwin-amd64 devbox-darwin-arm64
	makefat dist/devbox-darwin dist/devbox-darwin-amd64 dist/devbox-darwin-arm64

devbox-darwin.dmg: devbox-darwin
	./bin/create-dmg dist/devbox-darwin dist/$@

SHA256SUMS: binaries devbox-darwin.dmg
	shasum -a 256 dist/devbox-darwin.dmg > dist/$@

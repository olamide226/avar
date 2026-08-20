BINARY := avr
PKG    := github.com/olamide226/avar
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.DEFAULT_GOAL := build

.PHONY: build
build:
	go build -ldflags '$(LDFLAGS)' -o bin/$(BINARY) .

.PHONY: install
install:
	go install -ldflags '$(LDFLAGS)' .

.PHONY: test
test:
	go test -race ./...

.PHONY: cover
cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

# End-to-end tests against the real backend for this host: Lima on macOS, WSL 2
# on Windows. The build tags select the half that applies, so this is one target
# on both.
#
# Excluded from default CI: these create actual Linux environments (a virtual
# machine on one host, a registered distribution on the other), and the Windows
# half downloads a root filesystem the first time it runs.
#
# macOS needs limactl and virtualization; Windows needs WSL 2, and skips with a
# reason rather than failing if it is not there.
.PHONY: e2e
e2e:
	go test -tags=e2e -timeout=45m -count=1 ./e2e/...

.PHONY: lint
lint: fmt-check vet

.PHONY: vet
vet:
	go vet ./...

.PHONY: fmt
fmt:
	gofmt -s -w $(GOFILES)

# gofmt, unlike the go tool, walks into dot-directories. Prune them explicitly
# so build output and any nested checkouts are not linted as project source.
GOFILES = $(shell find . -type d \( -name '.*' -o -name dist -o -name bin \) -prune -o -type f -name '*.go' -print)

.PHONY: fmt-check
fmt-check:
	@unformatted=$$(gofmt -s -l $(GOFILES)); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt -s needed:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: tidy-check
tidy-check:
	go mod tidy -diff

.PHONY: release-version-test
release-version-test:
	bash scripts/next-version_test.sh

# Point git at the versioned hooks. Linked worktrees share the repository
# config, so this covers them too.
.PHONY: hooks
hooks:
	git config core.hooksPath .githooks
	@echo "hooks installed: direct pushes to main are now refused"

.PHONY: clean
clean:
	rm -rf bin coverage.out

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

# Real-Lima end-to-end tests. Requires macOS with virtualization and limactl.
# Excluded from default CI: these provision actual virtual machines.
.PHONY: e2e
e2e:
	go test -tags=e2e -timeout=30m -count=1 ./e2e/...

.PHONY: lint
lint: fmt-check vet

.PHONY: vet
vet:
	go vet ./...

.PHONY: fmt
fmt:
	gofmt -s -w .

.PHONY: fmt-check
fmt-check:
	@unformatted=$$(gofmt -s -l . ); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt -s needed:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: tidy-check
tidy-check:
	go mod tidy -diff

.PHONY: clean
clean:
	rm -rf bin coverage.out

# qsradio build system
#
# Primary targets:
#   make build     build for the current platform (development)
#   make dist      cross-compile for all release targets
#   make test      run the test suite
#   make check     vet + fmt + test (CI gate)
#   make clean     remove all build artefacts
#
# All builds use CGO_ENABLED=0. The binary has no C dependencies and
# cross-compiles without a C toolchain.

BINARY  := qsradio
CMD     := ./cmd/qsradio
DIST    := dist
MODULE  := github.com/muurk/qsradio

# Version: git tag on a tagged commit, short hash otherwise, 'dev' as fallback.
VERSION := $(shell git describe --tags --exact-match 2>/dev/null \
             || git rev-parse --short HEAD 2>/dev/null \
             || echo dev)

LDFLAGS  := -s -w -X 'main.version=$(VERSION)'
GOFLAGS  := -trimpath

# Release targets. The primary deployment target is linux/arm64 (Raspberry Pi 4+)
# and linux/arm/v7 (Raspberry Pi 2/3). The darwin targets support development on
# macOS. linux/amd64 covers x86-64 Linux servers and CI runners.
# windows/amd64 covers standard Windows desktops and laptops.
RELEASE_TARGETS := \
    linux/amd64    \
    linux/arm64    \
    linux/arm/v7   \
    darwin/amd64   \
    darwin/arm64   \
    windows/amd64

.PHONY: all build test vet fmt check dist clean help

## all: alias for build
all: build

## build: compile for the current platform
build:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

## test: run all package tests
test:
	go test ./...

## vet: run go vet across all packages
vet:
	go vet ./...

## fmt: verify formatting (non-zero exit if any file needs gofmt)
fmt:
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then \
		echo "unformatted files:"; echo "$$out"; exit 1; \
	fi

## check: vet + fmt + test (run before committing and in CI)
check: vet fmt test

## dist: cross-compile for all release targets; binaries land in dist/<os>-<arch>/
dist:
	@$(MAKE) $(foreach t,$(RELEASE_TARGETS),dist-$(subst /,-,$(t)))
	@echo ""
	@echo "dist complete: $(VERSION)"
	@find $(DIST) -name $(BINARY) | sort | xargs ls -lh

## dist-linux-amd64: linux x86-64
dist-linux-amd64:
	@mkdir -p $(DIST)/linux-amd64
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		go build $(GOFLAGS) -ldflags "$(LDFLAGS)" \
		-o $(DIST)/linux-amd64/$(BINARY) $(CMD)

## dist-linux-arm64: linux arm64 (Raspberry Pi 4/5, 64-bit OS)
dist-linux-arm64:
	@mkdir -p $(DIST)/linux-arm64
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
		go build $(GOFLAGS) -ldflags "$(LDFLAGS)" \
		-o $(DIST)/linux-arm64/$(BINARY) $(CMD)

## dist-linux-arm-v7: linux armv7 (Raspberry Pi 2/3, 32-bit OS)
dist-linux-arm-v7:
	@mkdir -p $(DIST)/linux-armv7
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
		go build $(GOFLAGS) -ldflags "$(LDFLAGS)" \
		-o $(DIST)/linux-armv7/$(BINARY) $(CMD)

## dist-darwin-amd64: macOS Intel
dist-darwin-amd64:
	@mkdir -p $(DIST)/darwin-amd64
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 \
		go build $(GOFLAGS) -ldflags "$(LDFLAGS)" \
		-o $(DIST)/darwin-amd64/$(BINARY) $(CMD)

## dist-darwin-arm64: macOS Apple Silicon
dist-darwin-arm64:
	@mkdir -p $(DIST)/darwin-arm64
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
		go build $(GOFLAGS) -ldflags "$(LDFLAGS)" \
		-o $(DIST)/darwin-arm64/$(BINARY) $(CMD)

## dist-windows-amd64: Windows x86-64
dist-windows-amd64:
	@mkdir -p $(DIST)/windows-amd64
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
		go build $(GOFLAGS) -ldflags "$(LDFLAGS)" \
		-o $(DIST)/windows-amd64/$(BINARY).exe $(CMD)

## clean: remove all build artefacts
clean:
	rm -f $(BINARY)
	rm -rf $(DIST)

## help: list available targets
help:
	@grep -E '^## ' Makefile | sed 's/^## //'

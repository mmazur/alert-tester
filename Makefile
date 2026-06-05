.PHONY: build

BUILD_REVISION := $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BUILD_MODIFIED := $(shell if git diff --quiet --ignore-submodules HEAD -- 2>/dev/null && git diff --cached --quiet --ignore-submodules -- 2>/dev/null; then echo false; else echo true; fi)
LDFLAGS := -X alert-tester/internal/buildinfo.revision=$(BUILD_REVISION) -X alert-tester/internal/buildinfo.modified=$(BUILD_MODIFIED) -X alert-tester/internal/buildinfo.buildTime=$(BUILD_TIME)

build:
	go build -ldflags "$(LDFLAGS)" -o atest ./cmd/atest

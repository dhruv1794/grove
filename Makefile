# grove — build and test targets.
#
# The binary's version is stamped at link time from `git describe`. When the
# tree isn't a git checkout (e.g. a source tarball), VERSION falls back to
# 0.0.0-dev, matching the default in internal/cli.Version. Override with
# `make build VERSION=v0.1.0`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
LDFLAGS := -X grove/internal/cli.Version=$(VERSION)

.PHONY: build test fmt clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/grove ./cmd/grove

test:
	go test ./...

fmt:
	gofmt -w .

clean:
	rm -rf bin

# Harbor — one Go binary with the SvelteKit build embedded.
#
# `make check` is the local equivalent of CI. If it is green here, it is green
# there; nothing merges that it has not run.

SHELL := /bin/bash
GO    ?= go
PNPM  ?= pnpm

MODULE  := github.com/AutumnsGrove/Harbor
BIN     := bin/harbor
VERSION ?= r$(shell git rev-list --count HEAD 2>/dev/null || echo 0).$(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(MODULE)/server/internal/build.version=$(VERSION)

.PHONY: version build dev test fuzz fmt-check vet check clean

version:
	@echo $(VERSION)

# The binary exists only inside the image and under bin/. Nothing here is
# committed (design.md §3).
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./server/cmd/harbor

test:
	$(GO) test -race ./...

# Go fuzzes exactly one target per invocation, so this is a script rather than a
# loop in the Makefile.
fuzz:
	./scripts/fuzz.sh

fmt-check:
	@unformatted=$$(gofmt -l $$(git ls-files '*.go')); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed:"; echo "$$unformatted"; exit 1; \
	fi

vet:
	$(GO) vet ./...

check: fmt-check vet test

clean:
	rm -rf bin

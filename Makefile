# Harbor — one Go binary with the SvelteKit build embedded.
#
# `make check` is the local equivalent of CI. If it is green here, it is green
# there; nothing merges that it has not run.

SHELL := /bin/bash
GO    ?= go
PNPM  ?= pnpm
IMAGE ?= harbor

MODULE  := github.com/AutumnsGrove/Harbor
BIN     := bin/harbor
VERSION ?= r$(shell git rev-list --count HEAD 2>/dev/null || echo 0).$(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(MODULE)/server/internal/build.version=$(VERSION)

.PHONY: version web build dev test fuzz fmt-check vet web-check worker-check rsync-check watcher-check packaging docker docker-run docker-check deploy check clean drill enrich-score enrich-record

version:
	@echo $(VERSION)

# The frontend build is produced here, in CI and in the Docker frontend stage —
# never committed. Only web/build/.gitkeep is tracked (design.md §3).
web:
	cd web && $(PNPM) install --frozen-lockfile && $(PNPM) build

# The binary exists only inside the image and under bin/. Nothing here is
# committed, and the frontend is embedded, so it is built first.
build: web
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./server/cmd/harbor

test:
	$(GO) test -race ./...

# The scored set is a paid, networked measurement, so it is never part of
# `check`: real saves with a human label, scored against the live prompt and
# model (plans/03, "Exit criteria"). -record refreshes the captures and leaves
# labels.json alone.
#
# The key is the one pi uses for its own tasks (~/.pi/agent/auth.json, mode 600),
# read for this target only and exported rather than put on a command line, so it
# stays out of `ps` and out of the repository. An unset key simply fails the
# command's own check.
SCORED_SET := server/internal/enrichscore/testdata/saves
PI_AUTH ?= $(HOME)/.pi/agent/auth.json
# Lower this while the set is still being labelled; the gate wants 20.
MIN ?= 20

enrich-score: export OPENROUTER_API_KEY ?= $(shell jq -r '.openrouter.key // empty' "$(PI_AUTH)" 2>/dev/null)
enrich-score:
	$(GO) run ./server/cmd/enrichscore -dir $(SCORED_SET) -min $(MIN)

# The phase-12a restore drill: a library, a backup, the data directory destroyed,
# a restore, and an export that agrees with it. Not part of `check`, because it
# runs a server and the point is the timing a human records (docs/runbook.md).
drill:
	./scripts/drill.sh

enrich-record:
	$(GO) run ./server/cmd/enrichscore -dir $(SCORED_SET) -record server/internal/enrichscore/testdata/sources.txt

# Local run: the identity lock is off (no Tailscale header on a laptop), and the
# dev defaults put data/ and update-signal/ beside the source. Run `make web`
# once first if the shell should render; a missing frontend answers 503.
dev:
	HARBOR_DEV_NO_AUTH=1 $(GO) run ./server/cmd/harbor serve

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

web-check:
	cd web && $(PNPM) check && $(PNPM) test

# The Worker is a second runtime with its own lockfile: typecheck plus the vitest
# suite that runs in workerd against real D1 and R2.
worker-check:
	cd worker && $(PNPM) install --frozen-lockfile && $(PNPM) typecheck && $(PNPM) test

# The update path is the one part of Harbor that can lose data. Its tests use
# fake docker/curl and never touch the network or the real .env.
# The rsync copy runs on the receiving machine, so its tests stub ssh and rsync
# rather than reaching for a real host.
rsync-check:
	./deploy/test-rsync.sh

watcher-check:
	./deploy/watcher/test.sh
	./deploy/firewall/test.sh

# Rules that CI must enforce and a human can break by accident. The frontend is a
# prerequisite because three of the rules inspect the shipped shell; `make check`
# must not be weaker than the CI job that runs the same script.
packaging: web
	./scripts/packaging-check.sh

# Local build of the same Dockerfile CI publishes. The potato never runs this.
docker:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

# Run the image the way a developer would: no Tailscale header, so auth is off
# and the deployment mode is dev (docker mode refuses HARBOR_DEV_NO_AUTH).
docker-run: docker
	docker run --rm --read-only --tmpfs /tmp \
		-p "127.0.0.1:8897:8080" \
		-e HARBOR_DEPLOYMENT=dev \
		-e HARBOR_DEV_NO_AUTH=1 \
		-e HARBOR_DATA_DIR=/data \
		-e HARBOR_UPDATE_DIR=/update-signal \
		-v "$(CURDIR)/data:/data" \
		-v "$(CURDIR)/update-signal:/update-signal" \
		$(IMAGE):latest

# Needs a built image: uid, no Node, /healthz and the container HEALTHCHECK.
docker-check: docker
	./scripts/image-check.sh $(IMAGE):latest

# The potato pulls; it never builds.
deploy:
	docker compose pull
	docker compose up -d

check: fmt-check vet test web-check worker-check rsync-check watcher-check packaging

clean:
	rm -rf bin web/.svelte-kit
	find web/build -mindepth 1 ! -name .gitkeep -delete 2>/dev/null || true

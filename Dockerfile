# syntax=docker/dockerfile:1.7
#
# Harbor ships as one container: the SvelteKit build is embedded in the Go
# binary, so there is no Node, no reverse proxy and no second process. The
# potato never builds — CI publishes this image and the watcher pins its digest
# (design.md §3).
#
# Build stages are pinned to BUILDPLATFORM so the frontend and the Go compile run
# natively once, not under QEMU per architecture. Only the distroless runtime is
# assembled per target.

# ── frontend ────────────────────────────────────────────────────────────────
FROM --platform=$BUILDPLATFORM node:22-bookworm AS web

WORKDIR /src
RUN corepack enable && corepack prepare pnpm@10.32.1 --activate

COPY web/ ./
RUN pnpm install --frozen-lockfile && pnpm build

# ── server ──────────────────────────────────────────────────────────────────
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS server

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# The context excludes web/build (source only), so the embed directory comes from
# the frontend stage. This is the only place the two halves are assembled.
COPY --from=web /src/build ./web/build

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
	go build -trimpath \
		-ldflags "-s -w -X github.com/AutumnsGrove/Harbor/server/internal/build.version=${VERSION}" \
		-o /out/harbor ./server/cmd/harbor

# ── runtime ─────────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.source="https://github.com/AutumnsGrove/Harbor" \
      org.opencontainers.image.title="Harbor" \
      org.opencontainers.image.description="A library that files itself."

# The data volume is owned by the same uid the host mounts; the compose file
# repeats it with HARBOR_UID/HARBOR_GID so a host mismatch is explicit.
USER 1000:1000

ENV HARBOR_DEPLOYMENT=docker \
    HARBOR_DATA_DIR=/data \
    HARBOR_UPDATE_DIR=/update-signal \
    HARBOR_PORT=8080 \
    TMPDIR=/tmp

COPY --from=server /out/harbor /app/harbor

EXPOSE 8080

# Distroless has no shell and no curl; the binary probes itself (design.md §3).
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
	CMD ["/app/harbor", "healthcheck"]

ENTRYPOINT ["/app/harbor"]
CMD ["serve"]

# syntax=docker/dockerfile:1
#
# One Dockerfile, one distroless image per binary in cmd/:
#
#   make image                      agentkit
#   make image BIN=fakellm          fakellm (T004)
#   make image TAGS=nopostgres      Go build tags, for variants without heavy backends
#
# The binary is the entrypoint and "serve" the default command, so
# `docker run <image> healthcheck` runs the self-check (there is no shell).

# GO_VERSION follows the toolchain line in go.mod (the Makefile passes it).
ARG GO_VERSION=1.27.2

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-mod=readonly
# Modules first, in their own layer: it is reused until go.mod or go.sum
# changes, and it survives a builder whose cache mounts were pruned. Behind a
# TLS-intercepting proxy, pass its CA as a build secret (make image
# BUILD_CA=/path/ca.crt); it is used for the download and kept out of the image.
COPY go.mod go.sum ./
RUN --mount=type=secret,id=ca,target=/etc/ssl/extra/ca.crt,required=false \
    SSL_CERT_DIR=/etc/ssl/certs:/etc/ssl/extra go mod download
COPY . .
ARG BIN=agentkit
ARG TAGS=""
ARG VERSION=dev
ARG COMMIT=""
ARG DATE=""
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -tags "$TAGS" \
      -ldflags "-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$DATE" \
      -o /out/app ./cmd/$BIN

FROM gcr.io/distroless/static:nonroot
ARG BIN=agentkit
ARG VERSION=dev
ARG COMMIT=""
ARG DATE=""
LABEL org.opencontainers.image.title="$BIN" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$COMMIT" \
      org.opencontainers.image.created="$DATE" \
      org.opencontainers.image.source="https://github.com/bsamanvith/agentkit" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /out/app /app
# Listen on every interface: the default (loopback) is unreachable through a
# port mapping. Mount the config here, readable by uid 65532.
ENV AGENTKIT_ADDR=:8080 \
    AGENTKIT_CONFIG=/etc/agentkit/config.json
EXPOSE 8080
USER 65532:65532
# SIGTERM exits 0 within AGENTKIT_SHUTDOWN_TIMEOUT (15s), above Docker's 10s
# default stop timeout: run with --stop-timeout 20 (Compose:
# stop_grace_period: 20s) so in-flight requests are not killed.
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD ["/app", "healthcheck"]
ENTRYPOINT ["/app"]
CMD ["serve"]

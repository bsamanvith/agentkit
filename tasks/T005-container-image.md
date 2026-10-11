---
id: T005
title: Distroless container image
epic: E01
status: done
depends_on: [T003, T004]
---

# T005: Distroless container image

## Context

[PLAN.md](../docs/PLAN.md) targets 10-20 MB images. Deployment shape is config, not image choice, so we ship one app image ([ENGINEERING.md §1.5](../docs/ENGINEERING.md#15-components-mix-and-match-deployment)).

## Scope

- Multi-stage `Dockerfile`: Go build stage with module and build caches, `CGO_ENABLED=0`, `-trimpath`, `-ldflags "-s -w"` + version info; final stage `gcr.io/distroless/static:nonroot`.
- Two images from the same file: `agentkit` (default) and `fakellm` (`--build-arg BIN=fakellm`).
- `--build-arg TAGS=...` passes Go build tags, for variants with heavy optional backends.
- Health checking without a shell: the binary supports `<bin> healthcheck` (hits its own `/healthz`), used by compose and k8s probes.
- `make image BIN=... TAGS=...` and `make images`.
- `.dockerignore`.

## Out of scope

- Publishing images to a registry (later, with release automation).
- SQLite needs CGO with some drivers: use a pure-Go driver, decided when `store/sqlite` lands. Note it here if it changes the image.

## Acceptance criteria

- [x] Each image builds and runs as non-root. Measure the size, set the budget in the Makefile, and record both in Notes (CI enforces it in T008). *`agentkit`: built, runs as uid/gid 65532, 17.7 MB, budget 20 MB (`IMAGE_BUDGET_MB`, `make image-size`). `fakellm` does not exist yet (T004): `make images` picks it up once `cmd/fakellm` exists, and T004 checks its image the same way.*
- [x] `docker run <image> healthcheck` exits non-zero when the server is not up and 0 when it is. *Docker: exit 1 in a fresh container, exit 0 inside the running one, `healthy` status. Unit: `TestHealthcheck`, `TestHealthcheckGivesUp`.*
- [x] A rebuild after changing one `.go` file reuses the module download layer. *Docker: `go mod download` step `CACHED`, Go build 2.2 s from the build cache.*

## Notes

### Files

- `Dockerfile`, `.dockerignore`, `Makefile` (image targets at the end), `deploy/example/{config.json,README.md}`.
- `cmd/agentkit/healthcheck.go` and `healthcheck_test.go`. In `main.go`: the `healthcheck` dispatch case and one usage line (nothing else).

### The image

- Build stage `golang:$GO_VERSION` (the Makefile passes the `toolchain` line of `go.mod`, so the two cannot drift; `GOTOOLCHAIN=local` makes a mismatch fail instead of downloading). `go mod download` runs in its own layer after copying only `go.mod`/`go.sum`. The modules live in the layer, not in a cache mount, so the layer is complete wherever it is reused (a CI layer cache, a pruned builder). The Go build cache is a cache mount. The build is T003's command: `CGO_ENABLED=0 go build -trimpath -tags "$TAGS" -ldflags "-s -w -X main.version=… -X main.commit=… -X main.date=…"`.
- Final stage `gcr.io/distroless/static:nonroot`, binary at `/app`, `USER 65532:65532` (numeric, so Kubernetes `runAsNonRoot` can verify it), `ENTRYPOINT ["/app"]`, `CMD ["serve"]`, so `docker run <image> healthcheck` replaces only the subcommand. `ENV AGENTKIT_ADDR=:8080 AGENTKIT_CONFIG=/etc/agentkit/config.json`, `EXPOSE 8080`. Docker `HEALTHCHECK` every 10 s, 3 s timeout, 5 s start period, 3 retries, running `/app healthcheck`.
- Build args: `BIN` (default `agentkit`; the package `./cmd/$BIN`), `TAGS`, `VERSION`, `COMMIT`, `DATE`, `GO_VERSION`. OCI labels: `org.opencontainers.image.{title,version,revision,created,source,licenses}`.
- `DATE` is the commit's date, not the build's: rebuilding a commit gives the same labels and binary.
- `.dockerignore` is an allowlist (`go.mod`, `go.sum`, `cmd/`, `internal/`, `pkg/`) that also drops `*_test.go` and `testdata/`: the context is the Go sources only, and editing a test does not rebuild the image.
- **Behind a TLS-intercepting proxy** (this sandbox has one) `go mod download` in the build container fails with `x509: certificate signed by unknown authority`. `make image BUILD_CA=/path/ca.crt` passes the CA as a BuildKit secret, used for the download only: it is not in any layer and does not change the cache key. Without `BUILD_CA` the secret is absent and nothing changes. The evidence below was built with `BUILD_CA=/root/.ccr/ca-bundle.crt`.
- Not pinned by digest: `golang` and `distroless` are pinned by tag. Pin digests (with automated updates) with release automation.
- SQLite: no change yet (no `store/sqlite`); a pure-Go driver keeps `CGO_ENABLED=0` and this image as is.

### Make targets

```
make image  [BIN=agentkit] [TAGS=...] [VERSION=...] [BUILD_CA=...]   tags $(BIN):$(VERSION) and $(BIN):latest
make images                                                         every binary in cmd/ that has an image (agentkit; fakellm once it exists)
make image-size [BIN=...] [VERSION=...] [IMAGE_BUDGET_MB=20]        fails over budget; T008 calls it in CI after make image
```

`VERSION` defaults to `git describe --tags --always --dirty`.

### Size and budget

| Measure | agentkit |
|---|---|
| Files, uncompressed (`docker export` of a created container, the budgeted number) | **17.7 MB** (17,740,288 B), of which the binary 14.4 MB and distroless 3.4 MB |
| Compressed (registry / pull size, `docker save`) | 5.9 MB |
| `docker image inspect .Size` | 26.0 MB: with the containerd image store it adds the compressed blobs to the unpacked files, so it is not used |

Budget: **20 MB uncompressed** (`IMAGE_BUDGET_MB ?= 20`), the top of PLAN.md's 10-20 MB target; 2.3 MB of headroom. The agent API (T036) will add to the binary: if it goes over, look at what grew (`go tool nm -size -sort size`) before raising the budget.

### Docker evidence

Docker 29.8.2 (daemon started in the session; containerd image store). Image built with `make image BUILD_CA=/root/.ccr/ca-bundle.crt` from `ee02b0f` plus this task's changes (hence `ee02b0f-dirty`). Outputs trimmed only where marked.

```
$ docker run --rm agentkit:latest healthcheck
agentkit: Get "http://127.0.0.1:8080/healthz": dial tcp 127.0.0.1:8080: connect: connection refused
[exit 1]
$ docker run --rm agentkit:latest                    # no config mounted
agentkit: config: open /etc/agentkit/config.json: no such file or directory
[exit 1]
$ docker run -d --name ak --stop-timeout 20 -p 18080:8080 -v $PWD/deploy/example/config.json:/etc/agentkit/config.json:ro agentkit:latest
$ docker inspect -f '{{.State.Health.Status}} user={{.Config.User}}' ak
healthy user=65532:65532
$ grep -E "^(Uid|Gid)" /proc/$(docker inspect -f "{{.State.Pid}}" ak)/status
Uid:	65532	65532	65532	65532
Gid:	65532	65532	65532	65532
$ docker exec ak /app healthcheck            → [exit 0], no output
$ docker exec ak /app healthcheck -ready     → [exit 0], no output
$ curl localhost:18080/healthz
{"status":"ok"}
$ curl localhost:18080/readyz
{"components":[{"name":"mcp-server","status":"ready"}],"status":"ready"}
$ curl localhost:18080/version
{"version":"ee02b0f-dirty","commit":"ee02b0fa16ce842619fa7affe3d8d254203ff28c","date":"2026-10-11T20:19:18Z"}
$ curl -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' localhost:18080/mcp/demo \
    -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"calculate","arguments":{"op":"mul","a":6,"b":7}}}'
event: message
data: {"jsonrpc":"2.0","id":4,"result":{"content":[{"type":"text","text":"{\"result\":42}"}],"structuredContent":{"result":42}}}
$ (same, tools/call echo {"text":"hello from the container"})
data: {"jsonrpc":"2.0","id":5,"result":{"content":[{"type":"text","text":"{\"text\":\"hello from the container\"}"}],"structuredContent":{"text":"hello from the container"}}}
$ time docker stop ak
real	0m0.192s
$ docker inspect -f 'exit={{.State.ExitCode}} oom={{.State.OOMKilled}}' ak
exit=0 oom=false
$ docker logs ak 2>&1 | tail -3
{"time":"2026-10-11T20:28:30.504716687Z","level":"INFO","msg":"listening","addr":"0.0.0.0:8080","components":["mcp-server"]}
{"time":"2026-10-11T20:28:46.477373162Z","level":"INFO","msg":"shutting down","timeout":"15s"}
{"time":"2026-10-11T20:28:46.477680062Z","level":"INFO","msg":"stopped"}

$ docker run -d --name akro --read-only --cap-drop ALL -p 18081:8080 -v …config.json:/etc/agentkit/config.json:ro agentkit:latest
$ docker inspect -f '{{.State.Health.Status}}' akro   → healthy
$ docker stop akro; docker inspect -f 'exit={{.State.ExitCode}}' akro   → exit=0

# Layer reuse: one line appended to cmd/agentkit/healthcheck.go, then make image (build steps only)
#11 [build 3/6] COPY go.mod go.sum ./
#11 CACHED
#12 [build 4/6] RUN --mount=type=secret,id=ca,… go mod download
#12 CACHED
#13 [build 5/6] COPY . .
#14 [build 6/6] RUN --mount=type=cache,target=/root/.cache/go-build … go build …
#14 DONE 2.2s

$ make image-size VERSION=latest
agentkit:latest: 17740288 bytes uncompressed, budget 20000000 (20 MB)          [exit 0]
$ make image-size VERSION=latest IMAGE_BUDGET_MB=17
agentkit:latest is over its size budget                                        [exit 2]
```

### `agentkit healthcheck`

```
agentkit healthcheck [-ready] [-timeout 2s]
```

- `GET /healthz` (liveness), or `GET /readyz` with `-ready` (200 also when degraded, as `/readyz` defines it), on `$AGENTKIT_ADDR` (default `127.0.0.1:8080`, serve's default). Exit 0 on 200, 1 otherwise, 2 on a usage error.
- A listen address on every interface (`:8080`, `0.0.0.0:8080`, `[::]:8080`) is checked on loopback (`127.0.0.1`, `[::1]`). An address with port 0 is an error: there is no fixed port to check.
- 2 s timeout by default, below the image's 3 s `HEALTHCHECK` timeout. Nothing is printed on success; one line on stderr on failure (Docker keeps it in `docker inspect` `.State.Health.Log`). No proxy, whatever `HTTP_PROXY` says.

Tests (`cmd/agentkit/healthcheck_test.go`), against the real `component` runtime's `/healthz` and `/readyz`:

| Criterion / behavior | Test |
|---|---|
| up (several address forms), ready, degraded counts as ready | `TestHealthcheck/up*`, `/degraded counts as ready`, `/unready but live` |
| unready with `-ready` | `TestHealthcheck/unready` |
| down (nothing listening) | `TestHealthcheck/down*` |
| wrong port (something else answers 404) | `TestHealthcheck/wrong port` |
| a server that never answers: fails after the default timeout | `TestHealthcheckGivesUp` |
| bad address or flags | `TestHealthcheck/no fixed port`, `/unparsable address`, `/positional argument`, `/non-positive timeout` |

Invariants checked after every run (in the `healthcheck` helper): exit 0 prints nothing; exit 1 says why on stderr; the command returns within 10 s.

Mutation self-check (`go test -race -count=1 -run Healthcheck ./cmd/agentkit/`, one plant at a time, file restored after each):

| Planted bug | Failing tests |
|---|---|
| always exits 0 (error from `healthcheck` dropped in `main`) | `TestHealthcheck/{down, down ready, unready, wrong port, no fixed port, unparsable address, positional argument, non-positive timeout}`, `TestHealthcheckGivesUp` |
| ignores `AGENTKIT_ADDR` (always the default) | `TestHealthcheck/{up…, unready but live, degraded counts as ready, timeout flag still succeeds}`, `TestHealthcheckGivesUp` |
| no timeout (`WithCancel` instead of `WithTimeout`) | `TestHealthcheckGivesUp` |
| `-timeout` ignored (fixed 1 minute) | `TestHealthcheckGivesUp` |
| `-ready` ignored (always `/healthz`) | `TestHealthcheck/unready` |
| any answer below 500 accepted | `TestHealthcheck/wrong port` |
| all-interfaces address not mapped to loopback | `TestHealthcheck/up, listening on all (:p)`, `/up, listening on 0.0.0.0` |
| prints on success | every `TestHealthcheck/up…` case |

No survivors. `go test -race -shuffle=on -count=20 -run Healthcheck ./cmd/agentkit/` is clean.

### For T006 (Compose)

- **Image:** `agentkit:latest` and `agentkit:$(git describe --tags --always --dirty)`, from `make image` (Compose can also `build: {context: ., args: {BIN: agentkit}}`; pass `GO_VERSION` from go.mod, or rely on the Dockerfile default, which must then match). `fakellm` the same with `BIN=fakellm` once T004 lands.
- **Env set by the image:** `AGENTKIT_ADDR=:8080`, `AGENTKIT_CONFIG=/etc/agentkit/config.json`. Set per service: `AGENTKIT_COMPONENTS` (default `mcp-server`), `AGENTKIT_LOG_LEVEL`, `AGENTKIT_SHUTDOWN_TIMEOUT`.
- **Port:** 8080 in the container; publish the fixed host ports from `deploy/local/README.md`.
- **Config:** mount read-only at `/etc/agentkit/config.json` (`./config/<topology>.json:/etc/agentkit/config.json:ro`), mode 0644 (the process is uid 65532). `deploy/example/config.json` is a working starting point (demo toolset at `/mcp/demo`).
- **Healthcheck:** the image's `HEALTHCHECK` already runs `/app healthcheck`. For `depends_on: condition: service_healthy` on readiness, override with `test: ["CMD", "/app", "healthcheck", "-ready"]`, `interval: 2s`, `timeout: 3s`, `retries: 15`, `start_period: 5s` (faster startup for `dev.sh up`). Exec form only: there is no shell, so `CMD-SHELL` fails.
- **Stop:** `stop_grace_period: 20s` (the shutdown deadline is 15 s; Docker's default is 10 s). An idle server stops in under 0.2 s with exit 0.
- **Hardening that works:** `read_only: true`, `cap_drop: [ALL]`, `user` already non-root.
- **Agent API (T036, not on `main` yet):** it selects with `AGENTKIT_COMPONENTS=agent` (or `agent,mcp-server`) and an `agent` section in the same config file. It refuses to start on a non-loopback address without `AGENTKIT_API_TOKEN`, and the image listens on `:8080`, so every service running `agent` needs `AGENTKIT_API_TOKEN` (and `ANTHROPIC_API_KEY`, or `fakellm` once T004 lands). Read T036's Notes for the final names when it lands.

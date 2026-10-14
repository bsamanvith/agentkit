# Image and binary sizes

Every change that moves an image's size by 100 KB or more adds a line here,
with the reason, in the same PR. A change that crosses a budget explains why
the size is worth it, or what it cuts first. See AGENTS.md, "Sizes".

## How to measure

```sh
make image BIN=agentkit && make image-size BIN=agentkit   # the budgeted number
```

`make image-size` counts the files of a created container, uncompressed
(`docker export | wc -c`). It is not `docker image inspect .Size`, which
depends on the image store. Budgets are `IMAGE_BUDGET_MB` in the Makefile.

To find what grew, build the binary with and without the suspect and compare.
Symbol tables (`go tool nm -size -sort size`) show where the code is, but miss
runtime metadata, which is about half of a Go binary:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o /tmp/agentkit ./cmd/agentkit
```

## What the agentkit image holds (2026-10-14, 22.2 MB)

| Part | Size | Why |
|---|---|---|
| distroless/static base | 2.1 MB | CA certificates, time zones, passwd |
| Go runtime, net/http, TLS, JSON | 7.2 MB | The floor for a Go server that calls HTTPS APIs |
| Anthropic SDK | about 4.6 MB | The model client, with its JSON and schema libraries |
| Postgres driver (pgx) | 4.1 MB | `agentkit migrate` (T088); the server store after T037 |
| MCP SDK | about 2.0 MB | `/mcp/...` and the tool protocol |
| agentkit's own packages | about 1.5 MB | harness, runner, store, connectors, CLI |
| The console (`pkg/web/dist`) | 0.7 MB | The embedded web UI |

The SDK numbers are approximate: they share code with the rest of the binary.
The Postgres and console numbers are exact (built with and without them).

## Log

Image sizes are uncompressed, as `make image-size` reports them.

| Date | Commit or PR | Image | Size | Change | Reason |
|---|---|---|---|---|---|
| 2026-10-11 | T005 | agentkit | 17,740,288 | first | Binary 14.4 MB; the rest, 3.4 MB, is distroless plus image metadata as T005 counted it (2.1 MB of distroless files on 2026-10-14). Budget set at 20 MB |
| 2026-10-12 | T029 | agentkit | 21.0 MB (measured, not merged) | +3.7 MB | SQLite (modernc, pure Go) wired into serve. Budget raised to 30 MB; wiring left to T037 |
| 2026-10-13 | `4af15e5` | agentkit | 17,378,816 | | Before the console |
| 2026-10-13 | T078 | agentkit | 18,099,712 | +720,896 | The console shell embedded (`pkg/web/dist`) |
| 2026-10-13 | T088 (`e197728`) | agentkit | 22,097,408 | +4.0 MB | The Postgres driver, linked by `agentkit migrate` |
| 2026-10-14 | T055, bsamanvith/agentkit#14 | agentkit | 22,162,944 | +65,536 | The console chat (lazy chunks, Markdown renderer) |
| 2026-10-14 | T037, bsamanvith/agentkit#15 (`b292d25`) | agentkit | binary 24,838,304 (before: 20,029,600) | +4.8 MB binary | SQLite and the stores in serve. Image projected at about 27 MB, under the 30 MB budget; not yet measured with `make image-size` |
| 2026-10-14 | T090, bsamanvith/agentkit#13 | agentkit | 30,360,064 (before: 28,246,528, main at `9cffbcd`) | +2,113,536 | Streams across replicas in serve: the Redis bus client (go-redis, 0.6 MB of symbols plus its runtime metadata), pkg/stream and pkg/bus. Binary 24,891,552 → 27,005,088. Measured as `make image-size` counts it, on the Dockerfile's final stage with a binary built as the Dockerfile builds it. Crosses the 30 MB budget, raised to 35 MB: several replicas need the bus, and serve is one image. What gets cut first: the Redis client from images that run a single replica, once there are per-component images |

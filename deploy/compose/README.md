# Docker Compose

`compose.yaml` is the split deployment of docs/COMPONENTS.md (the agent API
with the console, a runner, the toolsets) with Postgres, in Docker Compose.
It is the twin of [deploy/k8s/local](../k8s/README.md): the same images,
config, environment, security settings and limits. Use whichever you like;
the cluster's console is on port 30080 and this one on 30081, so both can run
at once.

## Two ways to run it locally

| | Compose (this folder) | Kubernetes ([deploy/k8s](../k8s/README.md)) |
|---|---|---|
| Needs | Docker Desktop (or any Docker with Compose v2) | Docker Desktop with Kubernetes turned on |
| Start | `make compose-up` | `make k8s-up` |
| Console | http://127.0.0.1:30081 | http://localhost:30080 |
| Smoke suite | `make compose-smoke` | `make k8s-smoke` |
| Delete it all | `make compose-down` | `make k8s-down` |

## Run it

```sh
export ANTHROPIC_API_KEY=sk-ant-...   # optional: without it, runs fail with model_auth
make compose-up
```

`make compose-up` builds `agentkit-api`, `agentkit-runner` and
`agentkit-toolsets` (`make images-split`), starts them with Postgres as the
Compose project `agentkit`, and returns when every service is healthy and the
console answers. Images are used from the local store, never pulled, except
`postgres:17-alpine` (and `busybox` for allinone) on the first `up`.

Open **http://127.0.0.1:30081** and paste the API token `make compose-up`
printed (`scripts/compose-local.sh token` prints it again).

| Command | Does |
|---|---|
| `make compose-up` | Build, start, wait. Run it again after a code change: it rebuilds and recreates the containers |
| `make compose-status` | Containers, their health, the console's URL |
| `make compose-smoke` | The smoke suite (test/e2e) against it: health, auth, console, toolsets, one chat run (calls the model) |
| `scripts/compose-local.sh logs api` | Follow a service's logs: `api` (with `migrate`), `runner`, `toolsets`, `postgres`, `allinone` |
| `scripts/compose-local.sh token` | The API token, for the console and `agentkit` CLI (`AGENTKIT_SERVER=http://127.0.0.1:30081`) |
| `scripts/compose-local.sh down` | Stop and remove the containers; keep the Postgres data and the secrets |
| `make compose-down` | `down -v`: also delete the volumes (Postgres data) and the secrets file |

`SKIP_BUILD=1 make compose-up` starts the images already built. Plain
`docker compose` works too, from this folder or with
`-f deploy/compose/compose.yaml`, once `up` has written the secrets file.

### All in one

`scripts/compose-local.sh up allinone` builds `agentkit-local` (`make
image-local`) and runs it alone: the agent API, console, turn loops and
toolsets in one process, on SQLite in the volume `allinone`, with the console
on **http://127.0.0.1:30082**. Same token; `scripts/compose-local.sh smoke
allinone` runs the suite against it. It is the Compose profile `allinone`,
so `make compose-up` never starts it.

## What runs

| Service | Image | Reserved | Limits |
|---|---|---|---|
| `migrate` | `agentkit-api`, `agentkit migrate`, once; `api` starts after it exits 0 | 0.01 CPU, 16 MiB | 0.2 CPU, 64 MiB |
| `api` | `agentkit-api`, `AGENTKIT_STORE_MIGRATE=off`, port 127.0.0.1:30081 | 0.02 CPU, 32 MiB | 0.5 CPU, 128 MiB |
| `runner` | `agentkit-runner` | 0.02 CPU, 32 MiB | 0.5 CPU, 128 MiB |
| `toolsets` | `agentkit-toolsets` | 0.02 CPU, 32 MiB | 0.5 CPU, 128 MiB |
| `postgres` | `postgres:17-alpine`, volume `agentkit_postgres` | 0.05 CPU, 64 MiB | 1 CPU, 256 MiB |
| `allinone` (profile) | `agentkit-local`, SQLite in volume `agentkit_allinone`, port 127.0.0.1:30082 | 0.02 CPU, 32 MiB | 1 CPU, 256 MiB |

Every agentkit container runs as the image's non-root user with a read-only
root filesystem, no capabilities and no privilege escalation, and has a
healthcheck: `/app healthcheck -ready`, the binary's own `/readyz` probe (the
images are distroless, with no shell or curl). The ports are bound to
127.0.0.1 only.

## Configs, secrets and mounts

- **Config**: every component mounts `deploy/k8s/base/config.json` (the
  agents and `mcp.toolsets`) at `/etc/agentkit/config.json`, through the
  Compose config `agentkit`. It is the same file the cluster's ConfigMap is
  made from and `make smoke` runs on, so edit it there; then `make
  compose-up` (or `scripts/compose-local.sh up` with `SKIP_BUILD=1`).
- **Files a toolset reads** go under `/etc/agentkit` beside the config, as a
  bind mount on `toolsets`, relative to `compose.yaml`. The one there is the
  `handbook` knowledge base's documents, the sample Northwind Bikes handbook
  that the agent `support` answers from:
  `volumes: [../local/agent/knowledge:/etc/agentkit/knowledge:ro]`. Edit the
  folder and restart `toolsets` to change them. The files must be readable by
  uid 65532.
- **Secrets**: `deploy/compose/.env` (mode 600, ignored by git) holds random
  RPC and API tokens, the Postgres password and the model key, as
  `AGK_RPC_TOKEN`, `AGK_API_TOKEN`, `AGK_PG_PASSWORD` and
  `AGK_ANTHROPIC_API_KEY`, and the credential vault's key ring
  `AGK_CREDENTIALS_KEY`. Compose reads it for the `${...}` values of
  `compose.yaml`, and each service gets only what it needs: the model key to
  the runner, the store URL to `migrate` and `api`. `up` writes it the first
  time and keeps it after that, since Postgres keeps the password it started
  with; a different `ANTHROPIC_API_KEY` on a later `up` replaces the stored
  key and the runner is recreated. `make compose-down` deletes it with the
  data. Nothing prints a secret except the API token.
- **Connector keys** (`TAVILY_API_KEY`, ...) go in the `toolsets` service's
  `environment`, and only there.
- **Connectors**: the agent `assistant` has every connector behind tool
  search, and each user connects their own accounts and keys on the
  console's **Connectors** page (the vault is in Postgres, or SQLite for
  the all-in-one). For the account connectors, register an OAuth app with
  each provider with the redirect URL `http://localhost:30081/oauth/callback`
  (`30082` for the all-in-one) and put `GOOGLE_OAUTH_CLIENT_ID` and
  `_SECRET` (likewise `HUBSPOT_`, `SALESFORCE_`) in `.env`
  (docs/QUICKSTART.md, "Connecting accounts").

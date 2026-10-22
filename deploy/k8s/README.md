# Kubernetes

- `base/`: the split deployment of docs/COMPONENTS.md (the agent API with the
  console, a runner, the toolsets), one Deployment and Service each, with no
  namespace and an in-memory store, and the ConfigMap made from
  `base/config.json`.
- `local/`: the base on Docker Desktop's Kubernetes, with Postgres, small
  resource limits and the console on a node port.

## Two ways to run it locally

The same split deployment also runs in Docker Compose, with no cluster:
`make compose-up`, console on http://localhost:30081
([deploy/compose/README.md](../compose/README.md)). Both use the same images,
config file and environment; this one is for manifests, probes and anything
that needs real Kubernetes.

## Run it on Docker Desktop

Turn on Kubernetes in Docker Desktop (Settings, Kubernetes) once. Then:

```sh
export ANTHROPIC_API_KEY=sk-ant-...   # optional: without it, runs fail with model_auth
make k8s-up
```

`make k8s-up` builds `agentkit-api`, `agentkit-runner` and
`agentkit-toolsets`, deploys them with Postgres to the namespace `agentkit`
of the `docker-desktop` context, and returns when every pod is ready. Docker
Desktop's cluster uses the local images directly: nothing is pushed or loaded.

Open **http://localhost:30080** and paste the API token `make k8s-up`
printed (`scripts/k8s-local.sh token` prints it again).

| Command | Does |
|---|---|
| `make k8s-up` | Build, deploy, wait. Run it again after a code change: it rebuilds and restarts the pods |
| `make k8s-status` | Pods, services, the console's URL |
| `make k8s-smoke` | The smoke suite (test/e2e) against it: health, auth, console, toolsets, one chat run (calls the model) |
| `scripts/k8s-local.sh logs api` | Follow a component's logs: `api` (with its migrate step), `runner`, `toolsets`, `postgres` |
| `scripts/k8s-local.sh token` | The API token, for the console and `agentkit` CLI (`AGENTKIT_SERVER=http://localhost:30080`) |
| `make k8s-down` | Delete the namespace: pods, secret and Postgres data |

`SKIP_BUILD=1 make k8s-up` deploys the images already built. `KUBE_CONTEXT`
picks another context; the script never creates a cluster and passes the
context to every kubectl call.

What runs, and what it may use:

| Pod | Image | Requests | Limits |
|---|---|---|---|
| `api` | `agentkit-api` (and its `migrate` init container) | 20m, 32 MiB | 500m, 128 MiB |
| `runner` | `agentkit-runner` | 20m, 32 MiB | 500m, 128 MiB |
| `toolsets` | `agentkit-toolsets` | 20m, 32 MiB | 500m, 128 MiB |
| `postgres-0` | `postgres:17-alpine`, 1 GiB volume | 50m, 64 MiB | 1 CPU, 256 MiB |

Idle, they use about 45 MiB together.

The secret `agentkit` holds random RPC and API tokens, the Postgres password
and URL, and the model key. It is made on the first `up` and kept after that,
since Postgres keeps the password it started with; a different
`ANTHROPIC_API_KEY` on a later `up` replaces the stored key. To change the
agents or toolsets, edit `base/config.json` (shared with Compose and
`make smoke`) and run `make k8s-up`.

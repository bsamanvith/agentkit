---
name: local-env
description: Run an agentkit change in a real deployment on this machine and check it end to end - pick the smallest way to run it (one process, the split deployment as processes, the image, or Docker Desktop's Kubernetes), bring it up, run the smoke suite, read the logs of a failing service, and tear it down. Use when a change needs checking beyond unit tests, when asked to run, start, try or smoke-test agentkit locally, or when make smoke or make k8s-up fails.
---

# local-env

**Goal: the change seen working in a running system, the failing service named when it is not, and the machine left as it was.** Why and the topologies: [ENGINEERING §3](../../../docs/ENGINEERING.md#3-local-environments-pods); the components and how they are wired: [COMPONENTS.md](../../../docs/COMPONENTS.md#topologies). There is no Compose and no `scripts/dev.sh`: T006 replaced them with the scripts below.

## 1. Pick the smallest way that exercises the change

| Way | Command | Needs | Use it for |
|---|---|---|---|
| Split deployment as processes | `make smoke` (`scripts/smoke.sh`) | Go, Node 22.18+ | Default. API + console, runner, toolsets as separate processes with the image's build tags, a fake model, the smoke suite (`test/e2e`). No Docker, no key |
| The same on Postgres | `AGENTKIT_STORE=postgres://... make smoke`, or `make integration STORE=postgres` | Postgres (`make integration` starts one as a process) | Store, migrations, row-level security |
| One process, real model | `scripts/try-agent.sh --demo` | `ANTHROPIC_API_KEY` (or `ANTHROPIC_KEY`) | A real agent run, tools, MCP at `/mcp/<toolset>` |
| The image | `scripts/try-agent.sh --docker --demo`; `make image` then `make image-size` | Docker daemon, a key | Dockerfile, `.dockerignore`, build tags, image size |
| Docker Desktop's Kubernetes | `make k8s-up`, `make k8s-smoke` | Docker Desktop with Kubernetes on, `kubectl` | Manifests, probes, Postgres in the cluster ([deploy/k8s/README.md](../../../deploy/k8s/README.md)) |

Check what the machine has before choosing (`docker info`, `kubectl config get-contexts`, `echo ${ANTHROPIC_API_KEY:+key}`). Cloud sessions have no Docker: use the first three ([AGENTS.md](../../../AGENTS.md#cloud-sessions)).

## 2. Bring it up and smoke it

- `make smoke` builds the three binaries, starts the fake model, toolsets, runner and API on `PORT` (default 18480; the model, toolsets and runner on the next three), waits for each `/readyz`, runs the suite, and stops everything. Its config is the one in `deploy/k8s/base/split.yaml`'s ConfigMap, so smoke and the cluster run the same agents and toolsets. `scripts/smoke.sh --keep` keeps serving afterwards and prints the console's URL and token; Ctrl-C stops.
- `make k8s-up` builds the split images, deploys, and returns when every pod is ready; `make k8s-smoke` runs the same suite against it (one chat run calls the real model).
- For a new user-visible feature, add or extend a flow in `test/e2e` only if a unit or replay test cannot show it ([§2.3](../../../docs/ENGINEERING.md#23-how-many-tests-one-per-behavior-not-per-function)).

## 3. When it fails, name the service

- **smoke.sh**: "`http://127.0.0.1:<port>/readyz did not answer`" names the port; map it with the table above (`PORT`+1 model, +2 toolsets, +3 runner, `PORT` the API). The script then prints the last 20 lines of each log under a `==> .../<service>.log <==` header: read the named service's first, then the services it calls (API → runner → toolsets, runner → model). A failing suite test also prints the logs.
- **k8s**: `make k8s-status` (a pod not `Running`, restarts, `Init:` for the API's migrate step), then `scripts/k8s-local.sh logs <api|runner|toolsets|postgres>`, and `kubectl --context docker-desktop -n agentkit describe pod <pod>` for events (image pull, OOM, probe failures).
- **try-agent.sh**: the server's output is in the terminal; `curl -s localhost:8080/readyz` lists each component's state.

Report the failing service, the log line that shows why, and what you changed or suggest.

## 4. Leave it as you found it

`make smoke` stops its processes itself. Check none is left with `pgrep -fl 'agentkit-(api|runner|toolsets) serve|fake-model'` (a bare `agentkit-` also matches shells in the repo's path) and that its ports no longer answer (`curl -s 127.0.0.1:$PORT/readyz` fails). Stop a `--keep` run or `try-agent.sh` with Ctrl-C (or `kill` the PID you started). `make k8s-down` deletes the namespace and its data; run it unless the user asked to keep the deployment.

## Troubleshooting

- **Port in use**: `PORT=19480 make smoke` (it uses `PORT` to `PORT+3`); `try-agent.sh` takes `PORT` too. Find the holder with `lsof -i :<port>` or `ss -ltnp`.
- **Stale data**: `make smoke` keeps nothing (memory store, temp dir). On k8s, `make k8s-down` then `make k8s-up` starts clean (the secret and Postgres volume go too).
- **No Docker or no cluster**: use `make smoke`; the k8s script says which context it could not reach.
- **Node too old** for the fake model: smoke needs Node 22.18 or later (`node --version`).
- **Real model**: `ANTHROPIC_API_KEY` (or `ANTHROPIC_KEY` in Claude sessions) for `try-agent.sh` and `make k8s-up`; without it k8s runs end with `model_auth`. `make smoke` never calls the real model.
- **A rebuilt image not picked up on k8s**: `make k8s-up` restarts the deployments; `SKIP_BUILD=1` reuses images already built.

## Done when

- The change was exercised in the smallest way that covers it, and the smoke suite passed there.
- A failure was traced to a named service with its log line.
- Nothing you started is still running, unless the user asked for it.

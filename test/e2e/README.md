# Smoke suite

A few black-box flows against a running deployment (T007): health and
readiness, auth, the console's page, the toolsets the API sees, and one chat
run that calls a tool and is kept by the store. It proves the components are
wired together; unit and integration tests prove the details.

| Command | Runs the suite against |
|---|---|
| `make smoke` | the split deployment as processes on this machine, on a fake model (`scripts/smoke.sh`; `--keep` keeps it serving so you can open the console). CI runs it |
| `make k8s-smoke` | `make k8s-up`'s deployment on http://localhost:30080; its chat run calls the real model |
| `make compose-smoke` | `make compose-up`'s deployment on http://localhost:30081 (`scripts/compose-local.sh smoke allinone`: the all-in-one on 30082); its chat run calls the real model |
| `scripts/smoke.sh --images` | the same, with the built `agentkit-api`, `agentkit-runner` and `agentkit-toolsets` images in place of the binaries (CI's image job, on Postgres) |
| `go test -tags e2e ./test/e2e/` | any deployment: `AGENTKIT_URL`, `AGENTKIT_API_TOKEN`; `AGENTKIT_SMOKE_CHAT=0` skips the chat run |

Each failure starts with the component it points at (`api`, `web`, `runner`,
`toolsets`, `store`). `TestComponents` (the per-image smoke, T102) checks
each process alone: `/readyz` lists its own components only, and it serves
its own surface (`/v1/agents` and `/`, `/rpc/runner/`, `/rpc/toolsets/` and
`/mcp/demo`) and none of the others'. It needs `AGENTKIT_SMOKE_RUNNER_URL`,
`AGENTKIT_SMOKE_TOOLSETS_URL` and `AGENTKIT_RPC_TOKEN`, which `make smoke` sets
and `make k8s-smoke` and `make compose-smoke` do not (the deployments do not
expose them), so it skips there. `scripts/smoke.sh` runs the suite with `-v` and
`AGENTKIT_SMOKE_STRICT=1`, under which a test that would skip fails, and fails
unless every test passed by name. A stopped runner fails `TestHealth` and `TestChat` with
`runner`, stopped toolsets fail `TestToolsets` and `TestChat` with `toolsets`.

Adding flows: at most a handful per epic, one per user-visible feature, each
against the API as a client sees it. No sleeps; read the run's stream to its end.

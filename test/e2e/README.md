# Smoke suite

A few black-box flows against a running deployment (T007): health and
readiness, auth, the console's page, the toolsets the API sees, and one chat
run that calls a tool and is kept by the store. It proves the components are
wired together; unit and integration tests prove the details.

| Command | Runs the suite against |
|---|---|
| `make smoke` | the split deployment as processes on this machine, on a fake model (`scripts/smoke.sh`; `--keep` keeps it serving so you can open the console). CI runs it |
| `make k8s-smoke` | `make k8s-up`'s deployment on http://localhost:30080; its chat run calls the real model |
| `go test -tags e2e ./test/e2e/` | any deployment: `AGENTKIT_URL`, `AGENTKIT_API_TOKEN`; `AGENTKIT_SMOKE_CHAT=0` skips the chat run |

Each failure starts with the component it points at (`api`, `web`, `runner`,
`toolsets`, `store`): a stopped runner fails `TestHealth` and `TestChat` with
`runner`, stopped toolsets fail `TestToolsets` and `TestChat` with `toolsets`.

Adding flows: at most a handful per epic, one per user-visible feature, each
against the API as a client sees it. No sleeps; read the run's stream to its end.

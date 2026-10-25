# Kubernetes

- `base/`: the split deployment of docs/COMPONENTS.md (the agent API with the
  console, a runner, the toolsets), one Deployment and Service each, with no
  namespace and an in-memory store, and the ConfigMap made from
  `base/config.json`.
- `local/`: the base on Docker Desktop's Kubernetes, with Postgres, small
  resource limits and the console on a node port.

## Two ways to run it locally

The same split deployment also runs in Docker Compose, with no cluster:
`make compose-up`, console on http://127.0.0.1:30081
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

Open **http://localhost:30080** and sign in as `admin` with the password
`make k8s-up` printed (`scripts/k8s-local.sh login` prints it again). The
console also takes the API token it prints, but the token has no user, so
it can't connect accounts or keys on the Connectors page.

| Command | Does |
|---|---|
| `make k8s-up` | Build, deploy, wait. Run it again after a code change: it rebuilds and restarts the pods |
| `make k8s-status` | Pods, services, the console's URL |
| `make k8s-smoke` | The smoke suite (test/e2e) against it: health, auth, console, toolsets, one chat run (calls the model) |
| `scripts/k8s-local.sh logs api` | Follow a component's logs: `api` (with its migrate step), `runner`, `toolsets`, `postgres` |
| `scripts/k8s-local.sh login` | The console's sign-ins: user `admin` and its password, and the API token |
| `scripts/k8s-local.sh token` | The API token, for the `agentkit` CLI (`AGENTKIT_SERVER=http://localhost:30080`) and the smoke suite |
| `make k8s-down` | Delete the namespace: pods, secrets and Postgres data |

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
and URL, the model key and the credential vault's key ring; the secret
`agentkit-console` holds the console's user `admin`: its password, and
`users.json` with its argon2id hash, which the API reads
(`AGENTKIT_USERS_FILE`). Both are made on the first `up` and kept after that,
since Postgres keeps the password it started with; a different
`ANTHROPIC_API_KEY` on a later `up` replaces the stored key. To change the
agents or toolsets, edit `base/config.json` (shared with Compose and
`make smoke`) and run `make k8s-up`.

The agent `assistant` has every connector (Gmail, Calendar, Drive, Sheets,
HubSpot, Salesforce, Exa, Parallel) behind tool search; each user connects
their own accounts and keys on the console's **Connectors** page, kept in the
vault in Postgres. Exa and Parallel need only a key. For the account
connectors, register an OAuth app with each provider with the redirect URL
`http://localhost:30080/oauth/callback` and export its variables
(`GOOGLE_OAUTH_CLIENT_ID` and `_SECRET`, likewise `HUBSPOT_` and
`SALESFORCE_`) before `make k8s-up`, which copies them into the secret and
restarts the API (docs/QUICKSTART.md, "Connecting accounts"). Unset, a
slot shows "Not set up". The toolsets pod has no vault: it reads each call's
credential from the API (`AGENTKIT_CREDENTIALS_URL`).

The agent `support` answers from the `handbook` knowledge base, the sample
Northwind Bikes handbook in `deploy/local/agent/knowledge`. `up` stores those
documents in the ConfigMap `agentkit-knowledge`, which the toolsets pod
mounts at `/etc/agentkit/knowledge`, and restarts the pods when they
changed (the runner and the API read the toolsets' list when they start). To serve your own documents, put Markdown files in that folder (it
must stay flat: a ConfigMap has no subfolders) and run `make k8s-up`.

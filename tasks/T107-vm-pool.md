---
id: T107
title: "VM pool: one microVM per session, warm across its runs, reclaimed when idle"
epic: E20
status: todo
depends_on: [T104, T079, T080]
---

# T107: VM pool

## Context

The strongest isolation of [E20](E20-runner-pools.md): each session (conversation) gets its own VM, so nothing it runs shares a kernel with another session. The VM stays up between the session's runs (its workspace persists) and is reclaimed when idle.

## Scope (to plan in detail, provider chosen with the owner)

- A `pool.Pool` with isolation `vm`: `Agent` finds the session's VM or boots one (Firecracker locally; a hosted provider later), then runs the agent there on the E16 runtime contract.
- Session affinity across replicas: the VM's address is stored with the conversation, so any replica's runner reaches it.
- Idle reclaim after a configurable time; a reclaimed session boots a new VM, with its workspace restored from the store or a volume.
- Credentials and tools as in T106.

## Acceptance criteria

- [ ] Two runs of one conversation run in the same VM; two conversations never share one.
- [ ] An idle VM is reclaimed after its timeout (fake clock), and the next run boots a new one.
- [ ] A VM lost mid-run leaves the run resumable.
- [ ] Mutation list recorded.

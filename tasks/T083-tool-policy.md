---
id: T083
title: "Tool policy engine: rules, presets, agent and environment policies, decisions in events"
epic: E17
status: todo
depends_on: [T082]
---

# T083: tool policy

## Context

Some runs may only read; some environments allow low-risk writes; some actions should be flagged or need approval. Decisions: [E17](E17-tool-safety-control.md) ("Policies", "Where policies come from").

## Scope

- **`pkg/policy`:**
  - `Policy` is an ordered list of `Rule{Match{Kinds, Risks, Tags, Tools, Toolsets}, Action}`, with a final default;
  - presets `read_only`, `low_risk_writes`, `approve_writes`, `all`;
  - `Narrow(agent, env) Policy`: for every call, the stricter decision of the two;
  - `Decide(def, call) Decision{Action, Rule, Reason}`.
- **Harness integration:** a `WithBeforeTool` hook (the harness already has it):
  - `deny` returns a tool error the model can read ("not allowed in this run: write tools are disabled");
  - `flag` lets the call run and emits a `tool_flagged` event;
  - `approve` hands off to T084. Until T084 lands, `approve` behaves as `deny` with a clear reason.
- **Events:** a `tool_policy` event for every decision other than plain allow (the event format is additive, per E09).
- **Config:** `agents[].tool_policy` (a preset name, or rules), plus an environment policy from `serve` config or the run request. The run request can only narrow.

## References

- AWS IAM policy evaluation (explicit deny wins; ordered statements) and OPA/Rego, for rule semantics; keep ours much smaller
- Claude Code permission rules (allow / ask / deny, per tool and pattern) for the user model
- OWASP Top 10 for LLM Applications: Excessive Agency

## Acceptance criteria

- [ ] **Presets:** a table test over each preset × kind × risk × tags gives the documented decisions.
- [ ] **Narrowing:** the agent's policy narrowed by `read_only` denies every non-read call, whatever the agent's policy says.
- [ ] **Run level** (fake model):
  - a denied call reaches the model as a tool error and the run continues;
  - a flagged call runs and emits `tool_flagged`;
  - the events show every decision.
- [ ] **Unclassified tools:** they are treated as `write`/`medium` (blocked by `read_only`).
- [ ] **Fuzzing:** `FuzzPolicy` (random rules and calls): `Narrow` is never more permissive than either input.
- [ ] **Mutation list** recorded.

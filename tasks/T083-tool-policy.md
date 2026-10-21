---
id: T083
title: "Tool policy engine: rules, presets, agent and environment policies, decisions in events"
epic: E17
status: done
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

- [x] **Presets:** a table test over each preset × kind × risk × tags gives the documented decisions. `pkg/policy.TestPresets` (5 kinds × 3 risks × 3 tag sets × requires_approval, against a literal table), `TestLowRiskWritesRules` (which rule decides), `TestUnclassifiedIsWriteMedium`.
- [x] **Narrowing:** the agent's policy narrowed by `read_only` denies every non-read call, whatever the agent's policy says. `pkg/policy.TestNarrowByReadOnly` (five permissive agent policies, both orders), and at run level `pkg/harness.TestToolPolicyInputNarrows`.
- [x] **Run level** (fake model):
  - a denied call reaches the model as a tool error and the run continues: `pkg/harness.TestToolPolicyDeniedCallIsAToolError`;
  - a flagged call runs and emits `tool_flagged`: `pkg/harness.TestToolPolicyFlaggedCallRuns`;
  - the events show every decision: `pkg/harness.TestToolPolicyEventsShowEveryDecision`; over the API (streamed and stored) `pkg/agentapi.TestRunRequestToolPolicy`; over the runner RPC `pkg/runner/runnerrpc.TestRemoteRunMatchesLocal/with_a_policy`; from the serve config `cmd/agentkit.TestServeToolPolicies`.
- [x] **Unclassified tools:** they are treated as `write`/`medium` (blocked by `read_only`). `pkg/policy.TestUnclassifiedIsWriteMedium`; at run level, the `legacy` tool of `TestToolPolicyDeniedCallIsAToolError`.
- [x] **Fuzzing:** `FuzzPolicy` (random rules and calls): `Narrow` is never more permissive than either input. `pkg/policy.FuzzPolicy` also checks it is exactly the stricter (the base on a tie), that every decision has a known action, its class and a reason, and that a valid policy decides the same after its JSON. Run for 30 s locally (340k execs, no failure).
- [x] **Mutation list** recorded (below).

Also: the invariants of the harness (`checkEvents`) now cover the policy events (between a call's start and done, never `allow`, no input, `tool_flagged` after a flag's `tool_policy` and before the result), and the seeded simulation (`TestSimulatedRuns`, `FuzzSimulatedRun`) runs a third of its scenarios with a policy that denies, flags and approves its tools and a fifth with a random preset in the input. The API's stream checker (`checkStream`) checks the same on the wire. A run's policy survives a crash and resume (`pkg/runner.TestToolPolicyKeptOnResume`); one whose stored policy cannot be read is not resumed without it (`TestToolPolicyUnreadableFailsTheResume`).

## Notes

### The API

`pkg/policy` (imports `pkg/tool` only):

```go
type Action string // Allow, Flag, Approve, Deny (least strict first); Actions(), Stricter(a, b)
type Match struct{ Kinds []tool.Kind; Risks []tool.Risk; Tags, Tools, Toolsets []string }
type Rule struct{ Name string; Match Match; Action Action; Reason string }
type Policy struct{ Name string; Rules []Rule; Default Action; NarrowedBy []Policy }
type Decision struct{ Action Action; Policy, Rule, Reason string; Kind tool.Kind; Risk tool.Risk }

func (p Policy) Decide(def tool.Definition, toolset string) Decision
func (p Policy) Validate() error // ErrInvalid
func (p Policy) Clone() Policy
func Narrow(base, by Policy) Policy
func Preset(name string) (Policy, error); func MustPreset(name string) Policy; func Presets() []string
// JSON: an object (unknown fields refused, validated on decode) or a preset's name as a string.
```

`pkg/harness`: `WithToolPolicy(p, toolsetOf func(name) string)`; `Input.ToolPolicy *policy.Policy` (narrows for one run); events `EventToolPolicy` and `EventToolFlagged` (appended after `EventRunDone`, so the numbers runnerrpc sends do not move) with `Event.Decision` and `Event.ToolUse` (ID and name only); `*PolicyError` is the refusal the model sees.

`pkg/runner`: `ToolPolicy(p) RunOption`, stored in the prompt entry's record (`PromptRecord.ToolPolicy`) like `FlushEvery`.

`pkg/agentapi`: `Runs.Start` takes `StartOptions{ToolPolicy}` (with `RunOptions()` for an orchestrator over a `runner.Runner`); `RunRequest.tool_policy`; event types `tool_policy` and `tool_flagged` with `action`, `policy`, `rule`, `reason`, `tool_kind`, `tool_risk` (plus `turn`, `call`, `tool_use_id`, `name`).

Config (`agentkit serve`): `agents[].tool_policy` (also in the legacy `agent` section), and a new top-level `"environment": {"tool_policy": ...}`. Documented in docs/QUICKSTART.md §5 "Tool policies" and docs/api/openapi.yaml (`ToolPolicy`, `ToolPolicyRule`, the event fields).

### Decisions

- **Presets** (literal table in `TestPresets` and QUICKSTART): `read_only` allows reads, denies the rest. `low_risk_writes`: rules `reads` (allow, any risk), `no_delete_send_execute` (deny), `no_high_risk` (deny), `low_risk_writes` (write/low allow), `medium_risk_writes` (write/medium approve), default deny. Reads are allowed whatever their risk in every preset, so `low_risk_writes` is never stricter than `read_only`. `approve_writes`: reads allowed, default approve. `all`: default allow. No preset looks at tags; tags are for custom rules.
- **Strictness** is deny > approve > flag > allow; an action outside the four ranks as deny, and `Decide` turns it into deny, so an unvalidated policy fails closed. On a tie in `Narrow` the base's decision (its rule and reason) is the one reported.
- **`Narrow` composes, it does not merge**: the result is the base with the other policy in `NarrowedBy`, evaluated separately. Merging ordered first-match lists exactly would need a product of rules. `NarrowedBy` is exported and in the JSON, so a narrowed policy round-trips; depth is capped at 8.
- **`Decide(def, toolset)`, not `Decide(def, call)`**: no rule looks at the call's input yet, and the toolset is what a rule needs that the definition does not carry. The harness gets it from `toolsetOf`; `cmd/agentkit` builds that from the agent's toolsets (a lookup for unprefixed names, the `<toolset>__` prefix otherwise, runtime toolsets included). A rule on toolsets never matches a tool whose toolset is not known.
- **`Match.Tools` globs** (`path.Match`) see the name as the model calls it and, for a prefixed name, the name within its toolset. **Tags match any of**, the other fields all must match, an empty field matches all.
- **`requires_approval`** is enforced now: a tool declaring it gets `approve` where a policy would allow or flag it, unless the deciding rule names the tool exactly (a glob without wildcards that equals one of its names). A wildcard does not count. With narrowing, every policy must name it to waive it. API, openapi.yaml and `tool.Metadata` docs now say so (they said "not enforced yet"). `interrupts` is still T084's.
- **Every agent gets a policy** in `serve`: `all` when it names none, so `requires_approval` is enforced by default; a harness built without `WithToolPolicy` (library users, `agentkit run`) enforces nothing unless the input has a policy. Names in decisions: a preset keeps its name; an unnamed agent policy is `agent`, the environment's `environment`, a run request's `run`.
- **Not a `WithBeforeTool` hook**, as the scope suggested: a hook can only deny, it cannot add events, and `flag` and every decision must be in the events. So the harness has its own `WithToolPolicy`, applied before the hooks (and the `WithAfterTool` hooks see a refusal like any result). The policy events come from the call's goroutine like `tool_start`/`tool_done`, between them.
- **Refusal text**: the model sees `not allowed in this run: <reason>`; for approve, plus `; the call needs a person's approval, which this run cannot ask for`. A decision's reason is the rule's `reason`, or made from the class: "medium-risk write tools are disabled (policy read_only)".
- **Environment policy** is a top-level `environment` section, not in `agent_api`: the legacy `agent` section embeds both the agent and the API fields, and a `tool_policy` in both would have been ambiguous. It narrows every agent's policy where its harness is built, so the runner component (split topology) applies it too.
- **Run request policy** is stored with the run's prompt entry, so a resume (recovery, interactive "continue", inline retry) narrows the same way. A resume whose prompt record cannot be decoded fails, not retryable, rather than run without it. `tool_policy` on a resume request (no message) is `400 invalid_request`: the run keeps its own.
- **Unknown tools** (called but not listed) are decided as unclassified (write/medium), so `read_only` refuses them before the registry says "not found".
- **Size**: no new dependency. Stripped `linux/amd64` binaries grew by 60–64 KB in every variant (agentkit 29,143,200 → 29,208,736; local +61,440; dev +65,536; api +61,440; runner +61,440; toolsets +65,536), under the 100 KB threshold, so no docs/SIZES.md row.

### Open points

- **Version skew in the split topology:** `runnerrpc` sends a run that carries a policy as its own method (`runner/RunWithPolicy`), so a runner older than this answers `rpc.unknown_method` and the run fails instead of running without the run's narrowing (it fails closed; runs without a policy still go to `runner/Run`). Deploy runners before the API. The agent and environment policies are built into the runner's harness from its own config, so they are not affected.
- `GET /v1/agents` does not show an agent's policy yet; a console (T087/T055) will want it.
- A policy cannot extend a preset ("low_risk_writes, but deny X"); today that is two policies narrowed, or the preset's rules written out. An `extends` field would be additive.

### For T084 (interrupts and approvals)

- `approve` is decided and reported now (`EventToolPolicy` with `Decision.Action == policy.Approve`), then refused in `Harness.decide` (`pkg/harness/tools.go`) with `PolicyError{NeedsApproval: true}`. Replace that refusal with the approval interrupt; the decision already carries the policy, rule and reason for the approval card.
- `autonomous` mode: "approve becomes deny" is what happens today; keep the reason text distinct.
- `requires_approval` already flows through the policy (see Decisions); a rule that names the tool exactly waives it.

### For T087 / T055 (console)

- Events `tool_policy` (action flag, approve or deny) and `tool_flagged` arrive between a call's `tool_call` and `tool_result`; a refused call's `tool_result` is the error the model saw. `policy`, `rule` and `reason` say why.

### Mutation list

Each bug planted alone, then `go test -count=1 -short` over the packages that use the code (script kept out of the repo). Every plant compiled and made a test fail. Four survived the first round (marked); a test was added for each and the four were caught on the rerun.

| Planted bug | Caught by |
|---|---|
| `Stricter` keeps the laxer action | `TestInvalidFailsClosed`, `TestNarrowByReadOnly`, `TestPresets` |
| an unknown action ranks as allow | `TestInvalidFailsClosed` |
| `Decide` ignores the narrowing policies | `FuzzPolicy`, `TestNarrowByReadOnly`, `TestJSON`, `ExamplePolicy_Decide`, harness `TestToolPolicyEventsShowEveryDecision`, `TestToolPolicyInputNarrows` |
| on a tie the narrower decides | `FuzzPolicy`, `TestNarrowByReadOnly` |
| match ignores kinds | `TestPresets`, `TestNarrowByReadOnly`, `TestUnclassifiedIsWriteMedium`, harness policy tests |
| match ignores risks | `TestPresets`, `TestRules`, `TestUnclassifiedIsWriteMedium`, `TestToolPolicyEventsShowEveryDecision` |
| tags must all match | `TestRules` |
| an unknown toolset matches toolset globs | `TestRules`, `TestToolPolicyEventsShowEveryDecision` |
| tool globs see only the full name | `TestRules`, `TestRequiresApprovalNeedsAnExactName` |
| requires_approval ignored | `FuzzPolicy`, `TestPresets`, `TestRules`, `TestRequiresApprovalNeedsAnExactName` |
| a wildcard overrides requires_approval (survived; `TestRequiresApprovalNeedsAnExactName` added) | `TestRequiresApprovalNeedsAnExactName` |
| an invalid action passed through | `FuzzPolicy`, `TestInvalidFailsClosed` |
| class read from the fields, not `Class()` | `FuzzPolicy`, `TestUnclassifiedIsWriteMedium`, `TestToolPolicyDeniedCallIsAToolError` |
| `read_only` flags writes | `TestPresets`, `TestNarrowByReadOnly`, `TestUnclassifiedIsWriteMedium`, harness tests |
| `low_risk_writes` allows high risk | `TestPresets` |
| `low_risk_writes` drops send from its deny rule (survived: the default denies it too; `TestLowRiskWritesRules` added) | `TestLowRiskWritesRules` |
| `approve_writes` allows writes | `TestPresets`, `TestUnclassifiedIsWriteMedium` |
| presets share their slices | `TestPresetsAreCopies`, `TestNarrowSharesNothing` and others |
| `Validate` skips the default | `TestValidate`, `TestInvalidFailsClosed`, `TestJSON`, `TestToolPolicyInputNarrows`, `TestRunRequestToolPolicyRefused` |
| `Validate` skips globs | `TestValidate`, `TestRunRequestToolPolicyRefused` |
| `Validate` skips depth | `TestValidate` |
| `Validate` skips the reason | `TestValidate` |
| `Clone` shares match slices | `TestNarrowSharesNothing`, `TestToolPolicyIsCopied` |
| `Narrow` changes its base | `TestNarrowSharesNothing` |
| JSON allows unknown fields | `TestJSON`, `TestLoadToolPolicies`, `TestRunRequestToolPolicyRefused` |
| JSON skips validation | `TestJSON`, `TestLoadToolPolicies`, `TestRunRequestToolPolicyRefused` |
| harness ignores the policy | harness, runnerrpc and agentapi policy tests |
| no `tool_flagged` event | `TestToolPolicyFlaggedCallRuns`, `TestToolPolicyEventsShowEveryDecision`, `TestSimulatedRuns`, `FuzzSimulatedRun`, `TestRemoteRunMatchesLocal`, `TestRunRequestToolPolicy` |
| approve runs the call | `TestToolPolicyEventsShowEveryDecision` |
| a denied call still runs | `TestToolPolicyDeniedCallIsAToolError`, `TestToolPolicyKeptOnResume`, `TestRunRequestToolPolicy` and others |
| input policy ignored | `TestToolPolicyInputNarrows`, `TestToolPolicyKeptOnResume`, `TestRemoteRunMatchesLocal`, `TestToolPolicyEventsShowEveryDecision` |
| input policy replaces the harness's | `TestToolPolicyInputNarrows`, `TestToolPolicyEventsShowEveryDecision` |
| input policy not validated | `TestToolPolicyInputNarrows` |
| `toolsetOf` not consulted | `TestToolPolicyEventsShowEveryDecision`, `TestServeToolPolicies` |
| `WithToolPolicy` keeps the caller's policy | `TestToolPolicyIsCopied` |
| policy event carries the call's input | invariants (`TestSimulatedRuns`, `FuzzSimulatedRun`) and the harness policy tests |
| `New` skips the policy check | `TestToolPolicyInputNarrows` |
| runner does not store the policy | `TestToolPolicyKeptOnResume` |
| resume ignores the stored policy | `TestToolPolicyKeptOnResume` |
| resume runs without an unreadable policy | `TestToolPolicyUnreadableFailsTheResume` |
| runner does not pass the policy to the agent | `TestToolPolicyKeptOnResume`, `TestRunRequestToolPolicy`, `TestRunRequestToolPolicyRules` |
| `runner.ToolPolicy` not validated | `TestToolPolicyInvalid` |
| runnerrpc drops the run's policy | `TestRemoteRunMatchesLocal` |
| runnerrpc drops decisions | `TestRemoteRunMatchesLocal` |
| API ignores the request's policy | `TestRunRequestToolPolicy`, `TestRunRequestToolPolicyRules`, `TestServeToolPolicies` |
| API takes a policy on a resume (survived: the existing case hit a finished conversation, refused anyway; `TestRunRequestToolPolicyNotOnResume` added) | `TestRunRequestToolPolicyNotOnResume` |
| API sends `tool_flagged` as `tool_policy` | `TestRunRequestToolPolicy` |
| wire: `tool_policy` marshals the header only | `TestRunRequestToolPolicy`, `TestRunRequestToolPolicyRules` |
| API: a request's policy not named `run` | `TestRunRequestToolPolicyRules`, `TestServeToolPolicies` |
| cmd: the environment does not narrow | `TestServeToolPolicies`, `TestLoadToolPolicies` |
| cmd: no toolsets for rules | `TestServeToolPolicies` |
| cmd: the agent's policy ignored | `TestServeToolPolicies`, `TestLoadToolPolicies` |

### Files

- New: `pkg/policy` (`doc.go`, `policy.go`, `presets.go`, tests), `pkg/harness/policy_test.go`, `pkg/runner/policy_test.go`, `pkg/agentapi/policy_test.go`, `cmd/agentkit/policy_test.go`.
- `pkg/harness`: `options.go`, `harness.go`, `tools.go`, `event.go`, `transcript.go` (`Input.ToolPolicy`), `errors.go` (`PolicyError`); tests `invariants_test.go`, `sim_test.go`.
- `pkg/runner`: `exec.go`, `record.go`, `cache.go`; `runnerrpc/runnerrpc.go` and its test.
- `pkg/agentapi`: `orchestrator.go` (`Runs.Start` takes `StartOptions`), `server.go`, `wire.go`, `events.go`, `wire/wire.go`, `wire/catalog.go` (doc); tests `orchestrator_test.go`, `helpers_test.go`.
- `pkg/tool/metadata.go` (the `RequiresApproval` doc).
- `cmd/agentkit`: `agent.go`, `serve.go`, `client.go` (prints flagged calls), `run.go` (prints policy events).
- `internal/archtest/rules.go` (`pkg/policy` and its importers), `docs/api/openapi.yaml`, `docs/QUICKSTART.md`.

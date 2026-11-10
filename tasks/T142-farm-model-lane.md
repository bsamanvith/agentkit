---
id: T142
title: The farm app's model lane: a list of models (Claude, OpenAI, OpenRouter, Gemini), each gated on make farm-eval, handing a request on when one fails
epic: E25
status: review
depends_on: [T133]
---

# T142: the model lane

## Context

The owner (2026-11-10): "For AI, we can have ai model, claude or openai is
ok for now. We can use openrouter for more cheaper alternatives in the
future if needed. ... keep it pluggable and switchable. Ensure that it's
properly handled." The roadmap's "Lanes" section: replace the hard-wired
"Claude, else Gemini" with a configurable list, and let a model in only
after `make farm-eval` passes on it. Free first; flag anything paid.

## Scope

- `apps/farm/models`: the provider table (anthropic, openai, openrouter
  through `pkg/llm/openaicompat`, gemini), model names
  `<provider>/<id>`, the record of evaluated models (`evaluated.json`),
  and `Lane`, an `llm.Model` that tries its models in order, hands a
  request on when a model fails before replying with an error another can
  avoid (overloaded, rate limited, a refused key), and rests the failed
  one for a while.
- `pkg/llm/gemini`: a step of tool calls another model made is sent with
  Gemini's documented placeholder signature, so Gemini can take over a
  conversation Claude started (the API refused it before).
- `apps/farm/cmd/farm`: `-models` (`$FARM_MODELS`, default
  `anthropic,gemini`, or `off`) and `-models-unevaluated`, in place of
  `-model` and `-model-id`; the lane's state logged at start.
- `make farm-eval`: `FARM_EVAL_MODEL` takes any lane model;
  `FARM_EVAL_RECORD=1` records a model that passes every scenario.

## Out of scope

- Picking the model per farmer or per skill (cheap model for routine
  skills), a status page for operators, cost tracking per farmer.
- OpenAI and OpenRouter runs of `make farm-eval`: no key in the cloud
  environment yet (both paid; OpenRouter's `:free` models need a free
  account key).

## Acceptance criteria

- [x] A lane list parses provider names, bare providers to their default model, OpenRouter ids with slashes; unknown providers, a bare OpenRouter and duplicates are refused — test: `models_test.TestParse`
- [x] Every provider needs its key, read from any of its variables — test: `models_test.TestBuildNeedsKey`
- [x] evaluated.json is valid, sorted, one entry per model, and recording a run replaces the model's earlier one — test: `models_test.TestEvaluatedFile`, `TestRecord`
- [x] The lane answers on its first model; an overloaded, rate-limited or refused model hands the same request to the next and rests (a minute; half an hour for a refused key), then is first again — test: `models_test.TestLaneUsesFirstModel`, `TestLaneHandsOn`
- [x] Errors the request causes, errors after the reply started and the last model's error reach the caller unchanged, nothing handed on — test: `models_test.TestLaneKeepsRequestErrors`
- [x] Models without a key or not evaluated are left out with a reason, in list order; an empty lane fails its requests — test: `models_test.TestLaneLeavesOut`, `TestLaneStatusesInOrder`
- [x] Gemini takes tool calls another model made (placeholder signature on the first call of an unsigned step) and keeps its own signatures — test: `gemini_test.TestForeignCallsSigned`; live: Claude to Gemini and back on a tool call (PR description)
- [x] When no model can answer (all busy, rate limited or refused), the farmer gets 503 "the assistant is busy", not an internal error — test: `farmapi_test.TestAssistant`; live with a bad Claude key (PR description)
- [x] `make farm-eval` passes on Claude and Gemini and records them — manual run in a cloud session (PR description)
- [ ] `make farm-eval` on an OpenAI and an OpenRouter model — needs their keys in the cloud environment

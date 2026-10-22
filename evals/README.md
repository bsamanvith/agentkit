# Tool-use evals

The connector kit (T049) proves a connector is correct. These evals measure
whether a **model uses it well**: does it pick the right tool, send arguments
that fit the question, stay within a few calls, cite what it found, and recover
after an error or a truncated result? Claude is real; the connectors are the
real ones, but their APIs are faked, so a run costs model tokens only and the
tool side is the same every time.

```sh
make eval                                   # every scenario, 3 runs each
EVAL_RUNS=5 EVAL_SCENARIOS='^tavily-' make eval
make eval-misleading                        # the proof that the checks can fail
```

It needs `ANTHROPIC_API_KEY` (or `ANTHROPIC_KEY`). It never runs in CI, like
the live tests. Settings: `EVAL_RUNS` (default 3), `EVAL_SCENARIOS` (a regular
expression on scenario names), `EVAL_MODEL` (default `claude-haiku-5-5`, the
deployed search agent's model), `EVAL_PARALLEL` (scenarios at once, default 4).

## How a run works

For each scenario, `TestConnectorEvals` (`eval_test.go`, build tag `eval`):

1. builds `agentkit` and starts `agentkit serve` with one agent. The agent has
   the deployed search agent's system prompt, low effort, and the scenario's
   toolsets. Each toolset's `base_url` points at the eval's faked upstream.
2. runs `agentkit ask -json` with the prompt, once per run, and reads the
   events: tool calls, results, the answer and the usage.
3. checks the transcript (`check.go`) and writes the report.

Nothing leaves the machine except the model calls:

- The server gets a dummy Anthropic key, and its model requests go to a local
  forwarder that adds the real key.
- Its `HTTPS_PROXY` and `HTTP_PROXY` are a proxy that refuses and records
  every request (`DenyProxy`). Loopback addresses bypass a proxy, so only a
  request to another host reaches it, and any such request fails the run.
- The connectors' keys are dummies too.

## Scenarios

`connectors/*.json`, one behavior each:

- `prompt`: the user message.
- `toolsets`: the connectors offered, `tavily`, `exa` or `parallel`.
- `prefix_tool_names`: whether tool names carry their toolset's prefix.
- `settings`: extra toolset settings.
- `upstream`: what the faked APIs answer, by toolset and endpoint, in order,
  with the last answer repeated. An answer is a recorded `cassette` (a T049
  cassette, `connectors/<p>/testdata/cassettes/*.res.http`), or a `status`,
  `headers` and a `body` or `body_file`. An endpoint left out answers with its
  default cassette (`DefaultResponses`; all about the Go 1.27 release).
- `checks`, with tool names given without a prefix:
  - `called` / `not_called` / `first`: which tools, and which first.
  - `max_calls`: the most tool calls allowed (0: none).
  - `args`: a field of the first call's input (or, with `every`, of every
    call's) that must be set, or `contains` some text, is `one_of` some
    values, is `at_least` a date, or is `absent`.
  - `cites_result_url`: the answer gives a URL that a tool result had.
  - `after`: after the first error (`on: "error"`) or truncation marker
    (`on: "truncation"`) in a result of `tool`, the next call is one of
    `next`. With `url_from_result`, that call must also hold a URL from that
    result.
  - `one_provider`: every call goes to one toolset.
- `misleading`: tool descriptions (by toolset, then tool) that should make the
  scenario fail. `make eval-misleading` runs only the scenarios that have
  them, with the descriptions swapped in through the toolsets' `descriptions`
  setting. Every check kind has one (`TestShippedScenarios` checks that), and
  each should drop to a low pass rate. That is how we know the check can fail.

Keep a scenario small and specific, so that a failure says what to fix. Most
often the fix is the tool's name, description or error message.

## Results

`make eval` prints a table and writes `results/<date>.json` (with `-2`, `-3`,
... for more runs that day, and `-misleading` for the proof runs). For each
scenario it gives the pass rate over the runs, the mean calls and tokens, and
every failed run with the checks it failed and its transcript.

Each new report is compared with the latest one before it of the same kind.
A scenario whose pass rate dropped is printed as `DROP`: shown, not gated.
Commit the results files you want to keep as a baseline.

Evals are noisy: judge pass rates over several runs, never one run.

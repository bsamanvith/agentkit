---
id: T017
title: "pkg/tool: definition, Func, registry, middleware, conformance suite"
epic: E03
status: done
depends_on: [T001, T002]
---

# T017: `pkg/tool`

## Context

The core abstraction from [PLAN.md](../docs/PLAN.md#tool). The harness, the MCP server, the `tools` component and the future remote client all go through it, so its interface must work both in-process and over a network.

## Design

- `tool.Tool` interface: `Definition() Definition` and `Invoke(ctx, input json.RawMessage) (json.RawMessage, error)`.
- `tool.Definition`: `Name`, `Description`, `InputSchema`, `OutputSchema` (schemas as `json.RawMessage`, so the public API does not depend on a schema library and is ready for the wire).
- `tool.Func[In, Out](name, description, fn)`: derives both schemas from Go types with `google/jsonschema-go`, validates input against the schema before calling `fn`, recovers panics.
- `tool.Registry` interface: `List(ctx) ([]Definition, error)` and `Invoke(ctx, name, input) (json.RawMessage, error)`. Both take `ctx` and return errors so a remote client can implement it.
- `tool.NewRegistry(tools, opts...)`: in-process registry, immutable after construction (safe for concurrent use without locks). Options: `WithMiddleware`.
- `tool.Middleware` + `tool.Timeout(d)`.
- Errors: `ErrInvalidDefinition`, `ErrInvalidInput`, `ErrToolFailed`, `ErrPanic`, `ErrNotFound`, `ErrDuplicateName`.
  - A failure inside the tool is `ErrToolFailed` and still unwraps to the tool's own error.
  - A cancelled or expired context returns the context error, never `ErrToolFailed`: the harness must tell "the run was stopped" apart from "the tool failed".
- Names must match `^[a-zA-Z0-9_-]{1,64}$`, the rule shared by MCP, Anthropic and OpenAI-compatible APIs.
- `pkg/tool/tooltest`: `RunRegistry(t, newRegistry)` conformance suite, plus the fixture tools it uses.

## Out of scope

- Auth requirements on tools, rate limiting (later middleware).
- Schema tweaks the Go types can't express (enums, min/max): add an option when the first real tool needs it.

## Acceptance criteria

Definition and `Func`:
- [x] The definition derived from a representative input/output struct matches a golden file (required vs optional fields, nested struct, slice, map, descriptions from tags, no extra properties allowed). — `TestFuncDefinitionGolden`
- [x] `Func` rejects at construction: an invalid name, a non-object input type, and an input type that has no JSON Schema (chan, func). Each is `ErrInvalidDefinition`. — `TestFuncRejectsInvalidDefinition`
- [x] Changing the returned `Definition` does not change the tool. — `TestDefinitionIsACopy`

Invoke:
- [x] Valid input reaches the function as a typed value and the output comes back as JSON. — `TestFuncInvoke`
- [x] Empty input is treated as `{}`. — `TestFuncEmptyInput`
- [x] Malformed JSON, a missing required field, a wrong type, and an unknown field are each `ErrInvalidInput`, and the function is not called. — `TestFuncInvalidInput`
- [x] An error from the function is `ErrToolFailed` and still matches the function's own error with `errors.Is`. — `TestFuncToolError`
- [x] A panic in the function is returned as `ErrToolFailed` + `ErrPanic`, not a crash. — `TestFuncPanic`
- [x] When the context is cancelled, the error is `context.Canceled` and not `ErrToolFailed`, even if the function wraps it. — `TestFuncContextCanceled`
- [x] No input bytes make `Invoke` panic; every error is `ErrInvalidInput` or `ErrToolFailed`. — `FuzzFuncInvoke`

Registry and middleware:
- [x] Construction fails with `ErrDuplicateName` for two tools with one name, and `ErrInvalidDefinition` for a custom tool with an invalid name. — `TestNewRegistryRejects`
- [x] Conformance suite passes for the in-process registry: list sorted by name, invoke routes by name, unknown name is `ErrNotFound`, invalid input / tool failure / cancellation keep their error kinds through the registry. — `TestRegistryConformance`
- [x] A panicking custom `Tool` (not built with `Func`) is recovered by the registry. — `TestRegistryRecoversPanics`
- [x] Middleware runs in the declared order, outermost first, and can see the tool's definition. — `TestMiddlewareOrder`
- [x] `Timeout` stops a slow tool with `context.DeadlineExceeded`; fast tools are unaffected. — `TestTimeout`
- [x] Concurrent `List` and `Invoke` from many goroutines are race-free. — `TestRegistryConcurrentUse` (meaningful under `-race`)
- [x] A runnable `Example` shows defining a tool and invoking it through a registry. — `ExampleFunc`

## Notes

How it was tested beyond the criteria:
- `go test -race -shuffle=on -count=50`: no failures (no flaky tests).
- `make fuzz FUZZTIME=60s`: ~1.9M inputs, no crashes.
- **Mutation check**: 16 bugs planted one at a time (skip validation, report cancellation as tool failure, drop panic recovery in `Func` / in the registry, reverse middleware order, unsorted list, allow duplicates, return shared `Definition` memory from `Definition()` / `List()`, reject empty input, misclassify tool errors, ignore `Timeout`, loosen the name rule, accept non-object input, lose the cause in `Error`). All 16 are caught.
  - The first run found two weak tests, both fixed: `TestDefinitionIsACopy` compared two values that shared memory, so it could not see a missing copy; `TestTimeout` hung instead of failing when `Timeout` did nothing (now bounded by a 5s cancel that produces a different error).
- Decisions:
  - Schemas are `json.RawMessage` in the public `Definition`, so callers do not depend on `jsonschema-go`.
  - Slices get schema type `["null","array"]` from `jsonschema-go` (a nil Go slice marshals to `null`). Revisit if a model provider rejects type arrays.
  - A cancelled context is checked before decoding, and again after a failed call: if the caller's context is done, the context error wins over `ErrToolFailed`. A tool's own internal timeout (caller context still live) is `ErrToolFailed`.
  - `Timeout` relies on tools honoring their context; it does not abandon goroutines.

Code review after the first push (all fixed, each with a test, and each new test checked by planting the bug it guards against):
- A panic's stack trace was part of the error message, which can reach a model or an MCP client. The stack now lives in `*tool.PanicError` (`errors.As`), and the message holds only the panic value.
- A custom `Tool` with a missing or non-object input schema was accepted, and would have been listed with `"inputSchema": null`. `NewRegistry` now rejects it with `ErrInvalidDefinition`.
- Removed a redundant conformance subtest (`OutputIsJSON` repeated what `InvokeRoutesByName` already proves).
- `TestTimeout` used a 20 ms limit that a fast tool could exceed on a slow runner; now 250 ms.

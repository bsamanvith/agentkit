# Invariants checked after every test

One helper builds the system under test and, when the test ends, checks what must hold for every run. A test then only states its own scenario; all of them check everything.

## The shape

```go
func newRig(t *testing.T, declaredKeys ...string) *rig {
	r := &rig{t: t /* recording fakes */}
	t.Cleanup(r.check) // runs after every test that built a rig
	return r
}
```

The fakes record what passes through them (requests sent, every saved state), so `check` can compare across the whole test, including across a crash and a resume.

## pkg/agentstep (T024): `helpers_test.go`

1. Every transcript the saver held is valid and a prefix of the next one for the same step. This catches a save before a turn is complete, and history edited after saving.
2. A finished harness run's `Result` transcript equals the saver's final transcript. This catches a skipped save.
3. Every `Output` in a final state holds a transcript that was saved, and its `Answer` is that transcript's last text.
4. A finished flow holds exactly the declared state keys. This catches a wrong output key or stray writes.

## pkg/harness (T022): `invariants_test.go`, `helpers_test.go`, `sim_test.go`

- Requests: each is a valid conversation, extends the previous one, and is never changed after sending (the recorder keeps two copies).
- Events: the grammar holds, every started tool call finishes, and text deltas add up to the committed text.
- Transcript: valid, starts with the input, round-trips through JSON (opaque bytes exact), and equals input plus announced messages.
- Record: usage adds up, model calls equal turns, and times never go backwards.
- The simulation (`sim_test.go`) runs 250 seeded scripts, plus `FuzzSimulatedRun`, through the same checks. A scribbling consumer must produce the same outcome as a clean one.
- `Validate` is judged against `referenceValid`, an independent restatement of the rules, on random conversations.

## cmd/agentkit replay e2e: `checkConversation`

Across all processes of a scenario, each agent's request extends its previous one byte for byte (so a thinking block read back from the state file is unchanged) and ends with a user message.

## How to find the invariants

Ask, for each entry point:
- What would a caller's data look like if it were corrupted? Make it check valid.
- What is promised to never change (history, sent requests)? Check it as a prefix.
- What must two paths agree on (events vs. result, saved vs. returned, interrupted vs. uninterrupted)? Check that they are equal.
- What must be released (goroutines, slots, connections)? Check it returns to baseline.

When a bug slips through, ask which invariant it broke and add that to the checks, not a test for the one case.

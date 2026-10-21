# review fixtures

Three changes that each add the same small package, `pkg/tally` (a counter
safe for concurrent use), and its task file `tasks/T900-review-fixture.md`.
They check the `review` skill (T015). Apply one in a scratch copy or worktree,
commit it, and review that commit; never push it.

```sh
git apply .agents/skills/review/testdata/planted.patch
git add -A && git commit -m fixture
```

| Patch | What is in it | A correct review reports |
|---|---|---|
| `planted.patch` | `pkg/tally` imports `cmd/reviewfixture/flags`; `TestConcurrentAdds` waits with `time.Sleep`; `TestEmptyNameIsRejected` compares `err.Error()` with a string; the criterion for `AddDefault` names `TestAddDefault`, which does not exist | all four as **blocking** |
| `weak.patch` | Tests pass, but `TestSnapshotIsACopy` still passes when `Snapshot` returns the internal map (the Notes claim it fails), and `TestConcurrentAdds` waits for the adds with a 70 ms wall-clock deadline (under `-race`, from about 1 run in 70 on an idle machine to 1 in 10 on a loaded one) | both as **blocking** |
| `clean.patch` | The same package done right | no blocking finding |

The patches only add files, so they apply to any later `main`. If one stops
applying, regenerate it from the files in the patch.

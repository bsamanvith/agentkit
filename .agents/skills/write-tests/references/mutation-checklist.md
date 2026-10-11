# Mutation self-check

Plant one bug at a time in the code under test, run the package's tests (`-race -shuffle=on -count=2`), record which tests fail, and restore the file. Diff it against a backup afterwards. A plant that does not compile does not count; rewrite it so it compiles.

## Bug classes to plant

- **Order:** results in finish order instead of call order; appended in the wrong order.
- **Sharing:** a value handed out without a copy (returned slice, event payload, stored input).
- **Lost or edited data:** a save skipped on one turn; an earlier message changed; an opaque block re-encoded or dropped.
- **Resume and retry:** restarting from scratch; sending the prompt again; re-running a finished step; saving a reply that ended in an error.
- **Keys and identity:** the output key ignored; a store key missing part of its identity (path, run id, tenant).
- **Limits:** a budget, a timeout or a concurrency cap off by one or not enforced; a slot never released.
- **Cancellation:** a missing `ctx` check; one more model call after cancel; goroutines not waited for.
- **Errors:** a wrong sentinel or class; a failure swallowed; a stack trace shown to the model.
- **Validation weakened:** malformed input accepted, or a half-written turn saved.

## A small driver (keep it out of the repo, in a directory of your own: agents sharing a scratch directory overwrite each other's files)

```python
plants = {"save skipped on turn 2": ("pkg/x/x.go", "old line", "new line"), ...}
for name, (f, a, b) in plants.items():
    orig = open(f).read(); assert orig.count(a) == 1
    try:
        open(f, "w").write(orig.replace(a, b))
        out = subprocess.run(["go", "test", "-race", "-count=2", "-timeout=5m", "./pkg/x/"], capture_output=True, text=True)
        o = out.stdout + out.stderr
        caught = sorted(set(re.findall(r"--- FAIL: (Test\w+)", o)))
        if "panic: test timed out" in o: caught.append("TIMEOUT")
        if "WARNING: DATA RACE" in o: caught.append("RACE")
        if "build failed" in o: caught = ["COMPILE ERROR (does not count)"]
        print(name, caught or "SURVIVED")
    finally:
        open(f, "w").write(orig)
```

## Recording it

Add a table to the task's Notes: planted bug, then the tests that caught it. See the tables in T022 and T024 (T024: 11 plants, plus 2 for the replay e2e). A survivor is fixed with a general check, then planted again.

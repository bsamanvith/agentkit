---
name: plan
description: Turn an idea, feature request or milestone into an agentkit epic and task files under tasks/, with acceptance criteria that are each one testable behavior. Use when planning new work, splitting a big change into tasks, writing or fixing a task file's criteria, or picking the next free task and epic IDs.
---

# plan

**Goal: an epic and a few task files that a fresh session can pick up and finish, each criterion one test.** Conventions: [tasks/README.md](../../../tasks/README.md); why criteria are the test list: [ENGINEERING §2.3](../../../docs/ENGINEERING.md#23-how-many-tests-one-per-behavior-not-per-function) and [§5](../../../docs/ENGINEERING.md#5-workflow-tasks-folder). Who plans and who implements: [WORKFLOW §1](../../../docs/WORKFLOW.md#1-roles).

Worked example: [E04](../../../tasks/E04-llm-harness-flow.md) (M3: `llm`, the Anthropic adapter, the harness, `flow`) and its first task [T020](../../../tasks/T020-llm-core.md): a design section the implementer can start from, criteria that each name their test, decisions recorded for the tasks that build on it.

## Steps

1. **Read before writing.** [docs/PLAN.md](../../../docs/PLAN.md) (scope and decisions), [ENGINEERING.md](../../../docs/ENGINEERING.md) §0 (build order) and §1 (layers), [tasks/STATUS.md](../../../tasks/STATUS.md), and every epic that touches the area: `grep -il '<topic>' tasks/*.md`. Extend an existing epic or task instead of duplicating it; reference the earlier task's Notes ("For T0xx") rather than restating them.

2. **Take the next free IDs.** `ls tasks | sort | tail` gives the highest `T` and `E` numbers; IDs are never reused, even for an abandoned task. Another branch may take the same number: the lint fails the second to merge, which renumbers.

3. **Ask only what changes the breakdown**: product direction, accounts and money, an irreversible choice, which of two designs. Everything else, decide and write the assumption in the epic, so the owner can overrule it by reading one place.

4. **Write the epic** (`E<NN>-<slug>.md`, front matter `id`, `title`, `status`): the outcome in a few lines, the decisions with their reasons, the ordered checklist `- [ ] [T0xx](T0xx-slug.md) <what>` (the lint checks it against each task's `status`), and a dependency sketch that says what can run in parallel (E04 has one).

5. **Write one file per task** from [_template.md](../../../tasks/_template.md): front matter (`epic`, `status: todo`, `depends_on` naming real IDs), Context that links docs instead of repeating them, Scope and Out of scope, Acceptance criteria, Notes. For a new package add a short Design section (interfaces, errors, subpackages) the `design-module` skill can start from.

6. **Make every criterion one observable behavior.** For each, ask "which single test fails if this is wrong?" and write its name: `— test: <pkg>.<TestName>`. Rewrite or split any criterion that is:
   - vague: "works well", "is robust", "handles errors";
   - implementation-shaped: "uses a mutex", "has a `Store` interface" (say what a caller sees instead);
   - several behaviors joined by "and" or by semicolons that would fail separately: one criterion each, or one table test whose rows are the cases of one rule (say so);
   - process, not behavior: "the mutation self-check is done", "`make check` passes". Every task does those (WORKFLOW §2); they are not criteria;
   - unrunnable here: a live key, Docker, a real account. Keep it, but say what it needs, so the implementer leaves it unticked with `status: review` (WORKFLOW §2 rule 5).

   Add criteria for the errors a caller must handle and for boundaries that bite (empty, cancel, concurrent). Code with many paths (state machines, concurrency, parsers) gets a criterion for its invariants and simulation ([§2.6](../../../docs/ENGINEERING.md#26-invariants-and-simulation-for-code-with-many-paths)).

7. **Keep tasks small: about one PR each**, roughly 5 to 10 criteria. A task that touches several packages with their own suites, or needs two kinds of reviewer, is two tasks. Flag anything bigger in the epic rather than hiding it in one file.

8. **Check.** `scripts/lint-skills.sh` (IDs, `depends_on`, links and anchors, the epic checklist). Then reread each criterion as the implementer: could you write its test without asking a question?

## Done when

- The epic and tasks lint clean, and every `depends_on` exists.
- Every criterion names one test (or says where it is covered, or what it needs to run).
- Assumptions and open questions are in the epic; questions for the owner are listed in your reply.
- `tasks/STATUS.md` and README status are the planner's: say what changed rather than editing them, unless you are the planner.

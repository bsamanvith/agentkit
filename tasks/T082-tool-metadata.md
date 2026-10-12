---
id: T082
title: "Tool metadata: kind, risk, interrupts, approval, tags, display; MCP annotations; connectors classified"
epic: E17
status: done
depends_on: [T017]
---

# T082: tool metadata

## Context

Policies (T083), interrupts (T084), the console's tool display and the agent builder all need tools to say what they do. Decisions: [E17](E17-tool-safety-control.md).

## Scope

- **New fields:** optional fields on `tool.Definition` (`Kind`, `Risk`, `Interrupts`, `RequiresApproval`, `Tags`, `Display{Title, Icon, Present, Past}`), with JSON names, validation, and `Clone` covering them. Definitions without them still validate.
- **Classification:** `tool.Classify(def) (kind, risk)` applies the default for unclassified tools (`write`/`medium`).
- **MCP mapping:** to and from MCP tool annotations (`readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`, `title`). `pkg/mcp/server` publishes them.
- **Built-in tools classified:** the demo tools; Tavily, Exa and Parallel (search and extract are `read`/`low`, tag `external`).
- **Kit check:** T049's kit gains a check (staged opt-in, then mandatory) that every connector tool declares `kind` and `risk`.
- **API:** `/v1/toolsets/{name}/tools` (T062) returns the new fields.

## References

- [MCP tools: annotations](https://modelcontextprotocol.io/specification/latest/server/tools) (`readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`)
- docs/UI.md "Tool calls" (the display fields the console needs)
- OWASP Top 10 for LLM Applications, "Excessive Agency" (why classes and least privilege)

## Acceptance criteria

- [x] **Round trip:** the new fields JSON round-trip, and `Clone` deep-copies them (a planted shared slice is caught). Tests: `pkg/tool.TestMetadataJSONRoundTrip`, `pkg/tool.TestCloneSharesNothing`, and `tooltest.RunRegistry`'s `ListKeepsMetadata` (every `Registry` implementation lists metadata whole, with tags it does not share).
- [x] **MCP mapping:** to and from annotations is lossless where both sides have the concept (table test), and `tools/list` over MCP shows the hints. Tests: `pkg/tool.TestAnnotationsTable`, `pkg/tool.TestAnnotationsRoundTrip` (all 192 combinations of kind, risk, mapped tags and title), `pkg/mcp/server.TestToolsListPublishesAnnotations`, the `tools_list_a` golden in `TestToolsetsAreIsolated`, and the kit's `mcp` check (each connector's listed annotations are its metadata's).
- [x] **Default class:** an unclassified tool is reported as `write`/`medium` by `Classify`. Test: `pkg/tool.TestClassify`; over the API, `pkg/agentapi.TestToolListMetadata`.
- [x] **Built-in tools:** all are classified, and the kit check passes for the three connectors. Tests: `pkg/toolset.TestDemoToolsAreClassified`; `TestConformance/classified` in `connectors/{tavily,exa,parallel}`; the check itself by `toolsettest.TestPlantedBugs/{no_risk,no_kind_under_setting}`.
- [x] **Mutation list** recorded (below).

Also: invalid metadata (unknown kind or risk, a malformed or repeated tag, a bad icon name, present without past, display text that is not one short line) is refused by both `Func` and `NewRegistry` with `ErrInvalidDefinition`: `pkg/tool.TestMetadataValidation`.

## Notes

### The API

`pkg/tool`:

```go
type Kind string // KindRead, KindWrite, KindDelete, KindSend, KindExecute; var Kinds
type Risk string // RiskLow, RiskMedium, RiskHigh; var Risks
const TagExternal, TagIdempotent = "external", "idempotent"

type Metadata struct {
	Kind             Kind     `json:"kind,omitempty"`
	Risk             Risk     `json:"risk,omitempty"`
	Interrupts       bool     `json:"interrupts,omitempty"`
	RequiresApproval bool     `json:"requiresApproval,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	Display          Display  `json:"display,omitzero"`
}
type Display struct{ Title, Icon, Present, Past string } // json: title, icon, present, past

type Definition struct {
	Name, Description, InputSchema, OutputSchema // as before
	Metadata                                     // embedded: d.Kind, d.Tags, d.Display...
}

func Classify(d Definition) (Kind, Risk)
func (m Metadata) HasTag(tag string) bool
func (m Metadata) Annotations() *Annotations    // to MCP; nil when MCP can express nothing
func FromAnnotations(a *Annotations) Metadata   // from MCP
type Annotations struct{ Title string; ReadOnlyHint bool; DestructiveHint *bool; IdempotentHint bool; OpenWorldHint *bool }

func Func[In, Out any](name, description string, fn func(context.Context, In) (Out, error), opts ...FuncOption) (*FuncTool[In, Out], error)
type FuncOption func(*Definition)
func WithMetadata(m Metadata) FuncOption
```

`tooltest.EchoMetadata()` is the metadata of the `echo` fixture.

### Decisions

- **`Metadata` is embedded in `Definition`.** Its fields read as the definition's own (`d.Kind`), its JSON is flat (`kind`, `risk`, ... next to `inputSchema`, camelCase like the rest of `Definition`), and one value can be passed around: `WithMetadata`, `FromAnnotations`, the policy engine. An unclassified definition's JSON is unchanged byte for byte (`Display` is `omitzero`), so nothing stored or golden moves.
- **Set on `Func` with an option.** `Func` gained a variadic `...FuncOption` after `fn` (source compatible). All three connectors wrap a `Func` tool (`checkedTool`, `classifying`, `classified`), and the wrappers pass the inner definition through, so the option covers them. There is no generic `WithMetadata(tool.Tool)` wrapper yet: nothing needs one.
- **Validation** is in `checkDefinition`, so `NewRegistry` refuses bad metadata from any tool, not only `Func` ones. Kind and risk are closed sets. Tags match `^[a-z0-9][a-z0-9_.-]{0,63}$`, with no repeats, so policy globs stay predictable. Icon is a name (`^[a-z][a-z0-9-]{0,31}$`) and is not checked against a set: docs/UI.md says an unknown name draws nothing. Present and past come together. Display text is one line of at most 80 characters.
- **`Classify` defaults each field on its own:** a tool that declares only `read` is `read`/`medium`. Only the declared value counts; nothing is inferred from tags or flags.
- **MCP mapping** (`Metadata.Annotations`, `FromAnnotations`):
  - `readOnlyHint` ↔ kind `read`.
  - `destructiveHint` is true for kind `delete` or risk `high`, false for another classified kind, and **unstated** for a read tool (MCP ignores it there) and for an unclassified tool (we do not know).
  - Back from MCP, `destructiveHint: true` is **`write`/`high`**, not `delete`: MCP's "destructive" covers overwrites as well as deletes, so the kind stays general and the risk carries the warning. A `delete` tool therefore comes back as `write`/`high`. The kind changes; the policy outcome does not, since `low_risk_writes` denies both.
  - `destructiveHint: false` is `write` with no risk (so `medium`).
  - An **unstated `destructiveHint` (and no `readOnlyHint`) is unclassified**, not MCP's default of true. This matches our own rule: a tool that says nothing is `write`/`medium`. It is also what makes our own unclassified tools round-trip. A server that says nothing gets `write`/`medium`, which a read-only policy still blocks.
  - `openWorldHint` ↔ tag `external`. It is false for a classified tool without the tag, unstated for an unclassified one, and only `true` reads back as the tag.
  - `idempotentHint` ↔ tag `idempotent`.
  - `title` ↔ `Display.Title`. We publish `annotations.title`, as docs/UI.md says, not the newer top-level `title`.
  - `Annotations()` is nil when there is nothing to say, so unclassified tools are listed without `annotations`, exactly as before.
  - Not expressible in MCP, so not published: `interrupts`, `requiresApproval`, other tags, risk `low` and `medium`, kinds `send` and `execute`.
  - `pkg/tool` mirrors the SDK's `ToolAnnotations` instead of importing it: internal/archtest allows `pkg/tool` only `jsonschema-go`.
- **The kit check `classified` lands opt-in, as the task says**, and the three connectors enable it. Under CONNECTORS.md's own rule it could have gone straight to opt-out, since every connector in the repository passes. But connectors outside the repository may use the kit, so it starts opt-in. **Next:** make it opt-out, then mandatory, and drop the `Enable` entries. The check looks at the tools built with the base settings and with each call's settings, because settings can change the tool set. It requires kind and risk, not display: display is optional (UI falls back to "Ran `name`").
- **The kit's `mcp` check also compares annotations** (listed vs `d.Annotations()`), since the definitions it compares now include them.
- **API** (`wire.Tool`, snake_case like the rest of the wire format):
  - `kind` and `risk` are the class as policies see it (`Classify`), always present;
  - `classified` is false when either was defaulted. A console can then say "Unclassified (treated as write/medium)" without restating the default;
  - `interrupts`, `requires_approval`, `tags` and `display` (`ToolDisplay`) are sent as declared, and omitted when empty.
- **The test fixtures:** `tooltest`'s `echo` fixture carries metadata (read/low, tags, display), so `RunRegistry` checks that every Registry implementation lists metadata whole and unshared. It leaves `interrupts` and `requires_approval` unset on purpose: the harness tests call `echo`, and T083/T084 would otherwise hold or hide it.

### How each tool is classified

| Tool | Kind / risk | Tags | Display (title · icon · present / past) | Why |
|---|---|---|---|---|
| `echo` (demo) | read / low | | Echo · tool · Echoing / Echoed | returns its input; changes nothing, reaches nothing |
| `current_time` (demo) | read / low | | Current time · clock · Checking the time / Checked the time | reads the clock |
| `calculate` (demo) | read / low | | Calculator · calc · Calculating / Calculated | pure arithmetic |
| `tavily_search`, `exa_search`, `parallel_search` | read / low | `external` | Web search (Tavily/Exa/Parallel) · search · Searching the web / Searched the web | a query against a public web index: no state changes anywhere, so `read`; the worst a wrong call does is spend a few cents and return irrelevant text, so `low`; it reaches the open web, so `external` (MCP `openWorldHint`) |
| `tavily_extract`, `exa_extract`, `parallel_extract` | read / low | `external` | Read web pages (Tavily/Exa/Parallel) · page · Reading pages / Read pages | fetches public pages the model names; reads only. The URL is model-chosen, so a policy that must not touch the web matches `external` |

None interrupts or requires approval. Each connector's tools are classified the same under every setting (Tavily's `model_chooses_depth`, Exa's `model_chooses_type`, Parallel's mode): the metadata is one package variable per tool. These are paid APIs; a `billing` tag was considered and left out until a policy needs it (E17 lists it only as an example).

### For T083 (the policy engine)

- Read a tool's class with **`tool.Classify(def)`**, never `def.Kind`/`def.Risk` directly: it applies the unclassified default (`write`/`medium`). `def.Kind == ""` tells you the tool was unclassified, if a rule or event wants to say so.
- Tags: `def.HasTag("external")`; the shared names are `tool.TagExternal` and `tool.TagIdempotent`. Tags are lower-case `[a-z0-9_.-]`, so glob matching needs no case folding.
- `def.RequiresApproval` is the tool's own default ("approve"), which a policy may override; `def.Interrupts` marks `ask_user`-like tools for T084's interaction modes.
- `tool.Kinds` and `tool.Risks` are ordered (increasing reach, lowest risk first) if "at most medium"-style rules want an order.
- Tools from an external MCP server: `tool.FromAnnotations(hints)` gives their class. Hints from an untrusted server are only claims.
- The agent API already shows `kind`, `risk` and `classified` per tool (`GET /v1/toolsets/{name}/tools`).

### For T055 (the console's tool display)

- `GET /v1/toolsets/{name}/tools` gives each tool's `display`: `title`, `icon` (a name from the console's set: the built-in tools use `search`, `page`, `calc`, `clock`, `tool`; an unknown name draws nothing), `present` ("Searching the web") and `past` ("Searched the web"). `display` is omitted when a tool declares none: fall back to "Ran `name`" with the first argument, as docs/UI.md says.
- `present` and `past` are always set together; each is one line, at most 80 characters, with no surrounding space.
- Tool names in a run are prefixed by the agent (`toolset__tool`): map them back to the toolset's list to find the display.
- Counts ("Read 2 pages") are the console's to add; the verbs are written without them.

### Files

- `pkg/tool`: `metadata.go` (new), `tool.go` (`Definition` embeds `Metadata`, `Clone`, `checkDefinition`), `func.go` (`FuncOption`, `WithMetadata`), `tooltest/tooltest.go` (`EchoMetadata`, `ListKeepsMetadata`); tests `metadata_test.go`, `registry_test.go`.
- `pkg/mcp/server`: `mcpserver.go` publishes annotations; `server_test.go`, `testdata/tools_list_a.golden.json`.
- `pkg/toolset`: `demo.go`; `toolset_test.go`.
- `pkg/toolset/toolsettest`: check `classified` (`checkid.go`, `checks.go`, `connector.go`), annotations in the `mcp` check (`mcp.go`, `checks.go`), toy bugs `no_risk` and `no_kind_under_setting` (`internal/toy/toy.go`, `toy_test.go`).
- `connectors/{tavily,exa,parallel}`: the metadata, `Enable: classified`, regenerated definition goldens.
- `pkg/agentapi`: `catalog.go` (`wireTool`), `wire/catalog.go` (`Tool` fields, `ToolDisplay`); `catalog_test.go`.
- `docs/api/openapi.yaml` (`Tool`, `ToolDisplay`), `docs/CONNECTORS.md` (the check's row).

### Mutation list

Each bug planted alone, then `go test -count=1` over the packages that use the file; every plant compiled and made a test fail.

| Planted bug | Caught by |
|---|---|
| Clone shares Tags (planted as `slices.Clip`) | `TestBuildConformance/ListKeepsMetadata`, `TestCloneSharesNothing`, `TestRegistryConformance/ListKeepsMetadata` |
| WithMetadata shares the caller's tags (survived the first run, when `Func` cloned the tags twice; the second clone was dropped and the mutant re-planted on the one left) | `TestCloneSharesNothing` |
| Classify defaults risk to low | `TestClassify`, `TestToolListMetadata` |
| Classify defaults kind to read | `TestAnnotationsRoundTrip`, `TestClassify`, `TestToolListMetadata` |
| Classify defaults only when both are empty | `TestClassify`, `TestToolListMetadata` |
| NewRegistry skips metadata checks | `TestMetadataValidation` |
| Func skips the definition check | `TestMetadataValidation` |
| unknown kind accepted | `TestMetadataValidation` |
| unknown risk accepted | `TestMetadataValidation` |
| duplicate tag accepted (planted as `m.Tags[i:i]`) | `TestMetadataValidation` |
| present without past accepted | `TestMetadataValidation` |
| title length off by one | `TestMetadataValidation` |
| control characters accepted (planted as `unicode.IsMark`) | `TestMetadataValidation` |
| tags JSON name | `TestDefinitions/definitions`, `TestMetadataJSONRoundTrip` |
| requiresApproval JSON name | `TestMetadataJSONRoundTrip` |
| display always written | `TestFuncDefinitionGolden`, `TestMetadataJSONRoundTrip` |
| readOnlyHint for every classified tool | `TestAnnotationsRoundTrip`, `TestAnnotationsTable/delete` |
| destructiveHint stated for a read tool | `TestAnnotationsRoundTrip`, `TestAnnotationsTable/read`, `TestToolsetsAreIsolated` |
| delete not destructive | `TestAnnotationsRoundTrip`, `TestAnnotationsTable/delete` |
| high risk not destructive | `TestAnnotationsRoundTrip`, `TestAnnotationsTable/write,_high_risk` |
| openWorldHint unstated for a classified tool | `TestAnnotationsTable/delete`, `TestToolsetsAreIsolated` |
| idempotentHint not published | `TestAnnotationsRoundTrip`, `TestAnnotationsTable/external_and_idempotent`, `TestToolsetsAreIsolated` |
| annotations for an unclassified tool | `TestAnnotationsTable/unclassified`, `TestToolsListPublishesAnnotations`, `TestToolsetsAreIsolated` |
| unstated destructiveHint read as true | `TestAnnotationsRoundTrip`, `TestAnnotationsTable/unclassified,_external` |
| destructive read back as delete | `TestAnnotationsTable/delete` |
| openWorldHint false read as external | `TestAnnotationsRoundTrip`, `TestAnnotationsTable/delete` |
| title not read back | `TestAnnotationsRoundTrip`, `TestAnnotationsTable/read,_external,_titled` |
| MCP server publishes no annotations | `TestConformance`, `TestFakeRunsTheSuite`, `TestGoldenFirstRun`, `TestToolsListPublishesAnnotations` |
| MCP server drops destructiveHint | `TestToolsListPublishesAnnotations` |
| MCP server drops openWorldHint | `TestConformance`, `TestFakeRunsTheSuite`, `TestGoldenFirstRun`, `TestToolsListPublishesAnnotations` |
| kit check: never fails | `TestPlantedBugs/no_kind_under_setting` |
| kit check: kind only | `TestPlantedBugs/no_risk` |
| kit check: risk only | `TestPlantedBugs/no_kind_under_setting` |
| kit check: default settings only | `TestPlantedBugs/no_kind_under_setting` |
| demo echo unclassified | `TestDemoToolsAreClassified` |
| demo calculate is a write | `TestDemoToolsAreClassified` |
| tavily extract unclassified | `TestConformance/classified`, `TestDefinitions/definitions` |
| exa search (model chooses type) unclassified | `TestConformance/classified` |
| parallel search (mode) unclassified | `TestConformance/classified` |
| parallel extract unclassified | `TestConformance/classified`, `TestDefinitions` |
| API: classified when either is declared | `TestToolListMetadata` |
| API: declared kind, not Classify (planted as `kind, risk := d.Kind, d.Risk`) | `TestToolListMetadata` |
| API: display always sent | `TestToolListMetadata` |
| API: tags dropped | `TestToolListMetadata` |
| API: interrupts dropped | `TestToolListMetadata` |

The kit's `mcp` check, which compares a connector's published annotations with `Definition.Annotations()`, cannot be broken through a connector (they all publish through `pkg/mcp/server`); the publishing itself is pinned at the source by `TestToolsListPublishesAnnotations` and by the three `MCP server ...` rows above, which the `mcp` check also catches.

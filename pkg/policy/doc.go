// Package policy decides which tool calls a run may make: a tool policy is an
// ordered list of rules over a tool's class (its kind and risk, see
// [tool.Metadata.Class]), its tags, its name and its toolset, each with an
// action, and a default for a call no rule matches (E17).
//
// The actions, from the least strict:
//
//   - [Allow]: the call runs;
//   - [Flag]: the call runs, and the run records that it was flagged;
//   - [Approve]: a person must approve the call before it runs;
//   - [Deny]: the call does not run; the model is told why, and can choose
//     something else.
//
// The first rule that matches decides. A tool that declares
// [tool.Metadata.RequiresApproval] needs approval even where a rule allows
// or flags it, unless that rule names the tool in [Match.Tools].
//
// Four presets cover the common cases ([Preset]): [ReadOnly], [LowRiskWrites],
// [ApproveWrites] and [All]. An unclassified tool counts as write/medium, so
// read_only blocks it until someone classifies it.
//
// Policies come from more than one place: the agent's, and the environment's
// or the run's, which may only narrow it. [Narrow] combines them: a call gets
// the strictest of their decisions, so the result is never more permissive
// than any of them.
//
// A [Policy] is a value: it holds no state and is safe for concurrent use.
// Its JSON form is the one config files and run requests use; a preset's name
// as a JSON string reads as that preset.
package policy

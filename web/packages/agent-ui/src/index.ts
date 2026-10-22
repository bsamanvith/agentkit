// @agentkit/agent-ui: agent components shared by the console and the
// consumer apps. They work from 360 px up, by touch, with nothing
// hover-only (docs/UI.md "Components"). The /v1 client is at
// "@agentkit/agent-ui/api".
// The Markdown renderer ("@agentkit/agent-ui/markdown") and the A2UI surface
// renderer ("@agentkit/agent-ui/a2ui") are their own entries, so apps can
// load them lazily; the surfaces' data (a2ui/model.ts) is here.
export {
  ToolStep,
  formatSeconds,
  stepText,
  toolKind,
  type ToolDisplay,
  type ToolKind,
  type ToolStepProps,
  type ToolStepState,
} from "./tool-step";
export { StepGroup, type StepGroupProps } from "./step-group";
export { SourceChip } from "./source-chip";
export { Message, type MessageProps } from "./message";
export { Composer, type ComposerProps } from "./composer";
export { UsageLine, usageText } from "./usage-line";
export {
  answerText,
  applyEvent,
  endAnswer,
  promptOf,
  sourcesOf,
  startAnswer,
  threadFromHistory,
  type AssistantTurn,
  type Part,
  type Step,
  type Turn,
  type UserTurn,
} from "./thread";
export {
  a2uiVersion,
  foldSurfaces,
  messagesOfInput,
  parseActionText,
  safeUrl,
  SurfaceEdits,
  uiToolName,
  withEdits,
  type Component as UIComponent,
  type Surface,
  type UIAction,
  type UIEntry,
  type UIMessage,
} from "./a2ui/model";

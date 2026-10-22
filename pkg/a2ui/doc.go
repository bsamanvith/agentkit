// Package a2ui is agentkit's side of A2UI, the declarative protocol with
// which an agent shows a person UI (forms, choices, cards) instead of text
// only: the message types, the catalog of components agents may use, the
// validator, and the ui_render tool that puts it in an agent's hands.
//
// # Version
//
// It targets the stable A2UI v0.9 protocol family, release v0.9.1 (the
// current production release, [SpecRelease]): every message carries
// "version": "v0.9" ([Version]), and every surface uses the basic catalog
// ([BasicCatalogID]). The message, field and component names come from the
// spec's own schemas (server_to_client.json, common_types.json and
// catalogs/basic/catalog.json); testdata keeps the copies the tests check
// against.
//
// # Messages
//
// A [Message] is one of the four server-to-client messages: createSurface,
// updateComponents, updateDataModel or deleteSurface. A surface's components
// are a flat list that refers to each other by ID, with one component named
// "root"; a client keeps them in a map and draws the tree from the root.
//
// # The catalog
//
// The catalog is the basic catalog without its media and overlay components:
// Text, Image, Icon, Row, Column, List, Card, Divider, Button, TextField,
// CheckBox, ChoicePicker and DateTimeInput ([Components]), with every function
// of the basic catalog. Slider, Tabs, Modal, Video and AudioPlayer are
// deferred ([Deferred]): rejected until a renderer draws them. Any renderer
// of the basic catalog draws what this package accepts.
//
// # Validation
//
// [Validate] checks a message, and [ValidateBatch] the messages of one
// ui_render call, against the spec and the catalog. It rejects an unknown
// component type or property, a wrong property type, a reference to a
// component that is not defined (a dangling child), a cycle, an unsafe
// literal URL (only absolute http and https URLs pass), and anything over
// the size limits ([MaxMessageBytes] and the others). Each problem is a
// [*ValidationError] with the JSON pointer of the offending field and a
// sentinel ([ErrUnknownComponent], [ErrUnknownProperty],
// [ErrDanglingReference], [ErrCycle], [ErrTooLarge], ...) that errors.Is
// matches; the message says what to change, for the model that wrote it.
//
// The harness has no state of the surfaces a conversation drew, so each call
// is checked on its own: a call that updates a surface's components defines
// its whole tree, root included (its updateComponents messages together), and
// every reference resolves within it. Clients merge the components into what
// the surface had, as the spec says, so a full tree each time is a valid
// stream.
//
// # The tool
//
// [Tool] is ui_render: its input is {"messages": [A2UI message, ...]}, an
// invalid input is a tool error naming each problem (so the model can fix
// it and call again), and a valid one succeeds. The agent API turns each
// message of a successful call into a "ui" event on the run's stream. Agents
// opt in with agents[].ui: "a2ui" ([UIMode]); others never see the tool.
//
// # Actions
//
// A person's action on a surface (a button) goes back to the agent as an
// [Action], the spec's client-to-server action message, in the next user
// message: [ActionText] writes it as structured text the model reads, and
// [ParseActionText] reads it back, for clients that show it.
package a2ui

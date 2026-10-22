// Package memory is the agent's context store: notes it keeps for itself,
// such as a user's preferences, facts about a project or progress on a long
// task, and reads back in later conversations.
//
// A [Store] holds files under [Root] ("/memories") and has the commands of
// Claude's memory tool: View (a file, or a directory listing), Create,
// StrReplace, Insert, Delete and Rename. [NewTool] makes them one tool,
// "memory", with that tool's input shape, so any model can use it and Claude
// can use it as it was trained to (and, with the Anthropic adapter's
// NativeMemory option, as the native memory tool type).
//
// # Scope
//
// Every call is scoped by the tenant in its context ([tenancy.Require]); a
// context with no tenant is [ErrNoTenant]. A store built with [ScopeUser] also
// keys every call by the user in the context ([tenancy.User]), and one with no
// user is [ErrNoUser]. Two scopes never see each other's files.
//
// # Paths
//
// Every path is [Root] or under it. [CleanPath] normalizes a path (repeated
// and trailing slashes, "." segments) and refuses one that is outside Root,
// has a ".." segment, a hidden name (one that starts with "."), a backslash,
// a control character, an escaped traversal ("%2e%2e") or is longer than
// [MaxPathBytes]. Nothing is resolved: a path with ".." is refused even when
// it would land inside Root.
//
// Directories are implicit: a directory exists while a file is under it.
// Root always exists and can be neither deleted nor renamed.
//
// # Limits
//
// [Limits] cap the bytes of one file, the bytes of all files of a scope and
// how many files a scope holds. A write that would pass one fails with an
// error matching [ErrLimit] and changes nothing. Every other failed command
// changes nothing either.
//
// # Search store
//
// An [Index] holds many items (an ID, a text, metadata and a time) and finds
// them by a [Query]: for recall over past conversations, documents and
// facts, where a Store holds a few notes read whole. It is scoped like a
// Store. A query has a [Mode]: [ModeKeyword] matches words and quoted
// phrases ([ParseKeywordQuery], words by [Tokenize]) and ranks by BM25;
// with an [Embedder] (IndexConfig.Embedder), [ModeSemantic] ranks by the
// cosine of embeddings and [ModeHybrid] fuses both rankings (reciprocal rank
// fusion, [RRFK] = 60). Each stored vector records its model and dimension;
// one of another embedder is [ErrEmbedderMismatch] until the index is
// re-embedded ([Reembedder]). A mode the backend lacks is
// [ErrModeUnsupported], never results of another mode. Package embed has
// the clients of embedding APIs (OpenAI-compatible, Voyage AI).
//
// [NewRecallTools] gives a model recall_search, recall_get and recall_save
// over an index. [AutoRecall] is a harness prepare hook (harness.WithPrepare)
// that adds the top results for each new user message to that message, in a
// <recalled source="memory"> block marked as data and not instructions; the
// system prompt never changes.
//
// # Backends
//
// Package inmem is the reference store and index, in memory; package sqlite
// keeps the files, and the items (searched through FTS5) and their vectors,
// in a SQLite file.
// Package memorytest is the conformance suite every backend passes:
// RunStore for stores, RunIndex (with a relevance set per mode) for indexes,
// with FakeEmbedder for the semantic modes.
package memory

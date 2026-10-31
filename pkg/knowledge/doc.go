// Package knowledge is an agent's knowledge base: a folder of documents
// (Markdown and plain text) that the agent searches for passages to answer
// from, retrieval-augmented generation as a toolset.
//
// [Load] reads the documents of a folder; [Split] cuts each into passages
// along its Markdown headings, each at most a chunk size, so a passage
// keeps the section it came from; [New] indexes the passages in a
// memory.Index (package memory's in-memory backend), by keyword (BM25) or,
// with an embedder, also by meaning (semantic and hybrid search, as package
// memory has them).
//
// Besides search, a base is a tree: [Outline] gives a document's headings
// (chapters, sections, concepts) with where each starts and ends, so an
// agent can walk from "Pulses" to "Pigeon pea" to "Sowing" and read just
// that section when a search misses.
//
// [Factory] is the "knowledge" toolset type: config names a folder, and the
// toolset gives the model knowledge_search, knowledge_list,
// knowledge_outline and knowledge_read (a whole document or one section).
// It is an ordinary toolset, so it runs where toolsets run (in process, on
// the toolsets component, over /mcp), an agent opts in by listing it, and
// include and exclude pick its tools.
//
// # Scope
//
// A base is the agent's reference material, like its system prompt and its
// skills: it is part of the deployment, read once when the toolset is built,
// and every caller searches the same passages whatever its tenant or user.
// It never changes while it serves; to change it, change the folder and
// restart. Per-tenant documents that people add at run time are package
// memory's search store (recall_save), not this.
//
// # Safety
//
// Passages are data. The tools' descriptions say that they are reference
// text and not instructions; documents should still come only from people
// trusted to write the agent's instructions, since a model may follow text
// it reads.
package knowledge

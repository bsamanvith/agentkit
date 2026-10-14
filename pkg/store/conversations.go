package store

import (
	"context"
	"time"
)

// Conversation is the record of a conversation: what a list of
// conversations shows, and the agent that runs it. A conversation's log
// ([ConversationStore]) exists without one; the record is what an API that
// creates, lists and deletes conversations keeps (T037).
//
// Rules, enforced by [ConversationIndex.CreateConversation] ([ErrInvalid]
// otherwise): ID is not empty, and ID, Agent and Title are text (see Text in
// the package documentation).
type Conversation struct {
	ID string
	// Agent names the agent that runs the conversation. It is a label: the
	// store does not interpret it. It never changes.
	Agent string
	// Title is what a list shows; empty until it is set
	// ([ConversationIndex.SetTitle]).
	Title string
	// Messages counts the messages of the conversation's log: those already
	// in it when the record was created, and every message appended since.
	// The store keeps it, in the same write as each append.
	Messages int64
	// CreatedAt is set by the store when the record is created. UpdatedAt is
	// set then, and again by every append to the log and every title set;
	// it never decreases.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ConversationPage is one page of [ConversationIndex.ListConversations].
type ConversationPage struct {
	Conversations []Conversation
	// Next is the cursor of the following page, or empty if this page reached
	// the end. A page with a Next holds exactly the page's limit.
	Next string
}

// ConversationIndex keeps the records of conversations. Implementations must
// be safe for concurrent use, must honor ctx, must scope every call by the
// tenant in ctx, and must not keep or share memory with values passed in or
// returned. Run storetest.RunConversationIndex against every implementation,
// on a value that is also the backend's [ConversationStore] and [RunStore]:
// the record counts the log's messages, and deleting a conversation deletes
// its log and runs.
type ConversationIndex interface {
	// CreateConversation stores a record for c.ID with c.Agent and c.Title,
	// and returns it as stored; the other fields of c are ignored. Creating
	// a record that exists with the same Agent returns the stored record
	// unchanged, so a retry is safe; one with another Agent is
	// [ErrConflict].
	CreateConversation(ctx context.Context, c Conversation) (Conversation, error)
	// GetConversation returns the record, or [ErrNotFound].
	GetConversation(ctx context.Context, id string) (Conversation, error)
	// ListConversations returns one page of the records, of every agent or
	// only of agent, ordered by CreatedAt and then ID. Paging visits every
	// record that existed when the first page was read, and was not deleted
	// since, exactly once. FromSeq is [ErrInvalid] here.
	ListConversations(ctx context.Context, agent string, p Page) (ConversationPage, error)
	// SetTitle sets the record's title if it is empty, and returns the
	// record: of two first messages, the first title stays. A title that is
	// empty, or a record that has one, changes nothing. [ErrNotFound] if
	// there is no record.
	SetTitle(ctx context.Context, id, title string) (Conversation, error)
	// DeleteConversation deletes the record, the conversation's log, its
	// runs, and those runs' event logs and checkpoints, all or none. It
	// fails with [ErrNotFound] if there is no record, and with [ErrLeased]
	// while a run of the conversation that is not final is leased (a
	// process runs it): stop it first.
	DeleteConversation(ctx context.Context, id string) error
}

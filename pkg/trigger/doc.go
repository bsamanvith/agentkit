// Package trigger is what starts background runs without a person (T072): a
// trigger fires on a cron schedule, or when its inbound webhook is called,
// and each firing starts a run of an agent on a prompt.
//
// The package holds the parts that do not serve HTTP or start runs:
//
//   - [Definition], a trigger's definition as the store keeps it
//     (store.TriggerRecord.Data), with its prompt template ([Definition.Render]).
//   - [Schedule], a cron schedule in a time zone (CRON_TZ=), and its
//     daylight-saving rules ([Schedule.Next]).
//   - [Message], the message a firing's run starts from, with a webhook's
//     body after the prompt as a labelled third-party data block, never as
//     instructions.
//   - [Scheduler], the loop that finds due triggers across tenants
//     (store.TriggerAdmin) and fires them.
//   - [VerifyStandard] and [SignStandard], the Standard Webhooks signatures
//     inbound webhooks carry (https://www.standardwebhooks.com/).
//
// The agent API (package agentapi) serves /v1/triggers and the inbound
// webhooks (/v1/hooks/{id}), and fires triggers: it claims each firing in
// the store (store.TriggerStore.StartFiring), so each due time and each
// webhook delivery fires once however many replicas share the store; a
// trigger whose previous firing's run is still going on records the firing
// as skipped; and after downtime a schedule fires once for all the times it
// missed, then goes on from now.
package trigger

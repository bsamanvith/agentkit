package memorytest

import (
	"slices"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/memory"
)

// Relevance is a fixed search-quality set: items, and queries with the hits
// a backend must rank at the top.
type Relevance struct {
	Mode    memory.Mode
	Items   []memory.Item
	Queries []RelevanceQuery
}

// RelevanceQuery is one query of a [Relevance] set.
type RelevanceQuery struct {
	Query memory.Query
	// Top are the IDs the results must start with, in this order.
	Top []string
	// Absent are IDs that must not be among the results at all.
	Absent []string
	// Why says what the query checks.
	Why string
}

// RunRelevance puts set's items in a new scope of x and checks each query.
func RunRelevance(t *testing.T, x memory.Index, set Relevance) {
	t.Helper()
	ctx := Ctx(t, "relevance", "u")
	for _, it := range set.Items {
		if err := x.Put(ctx, it); err != nil {
			t.Fatalf("Put(%s): %v", it.ID, err)
		}
	}
	for _, rq := range set.Queries {
		q := rq.Query
		q.Mode = set.Mode
		if q.K == 0 {
			q.K = 10
		}
		res, err := CheckSearch(ctx, t, x, q)
		if err != nil {
			t.Errorf("%s: %v", q.Text, err)
			continue
		}
		got := IDs(res)
		if len(got) < len(rq.Top) || !slices.Equal(got[:len(rq.Top)], rq.Top) {
			t.Errorf("%s (%s): top %v, want %v first", q.Text, rq.Why, got, rq.Top)
		}
		for _, id := range rq.Absent {
			if slices.Contains(got, id) {
				t.Errorf("%s (%s): %v has %s, which must not match", q.Text, rq.Why, got, id)
			}
		}
	}
}

// KeywordRelevance is the relevance set of ModeKeyword: 26 notes of a team,
// and 10 queries where exact phrases come first, rare words outweigh common
// ones, and case, punctuation and scripts other than Latin work.
func KeywordRelevance() Relevance {
	at := func(d int) time.Time { return time.Date(2026, 5, d, 9, 0, 0, 0, time.UTC) }
	items := []memory.Item{
		{ID: "deploy-bg", Text: "We ship the API with a blue green deployment: the new version goes to the idle colour, then traffic switches."},
		{ID: "deploy-colours", Text: "The deployment dashboard shows green when healthy and blue while a rollout is paused."},
		{ID: "deploy-canary", Text: "Canary deployment sends five percent of traffic to the new version first."},
		{ID: "deploy-friday", Text: "Team rule: don't deploy on Fridays after noon."},
		{ID: "deploy-checklist", Text: "Deploy checklist: run migrations, warm caches, then deploy the workers."},
		{ID: "pg-backup", Text: "The Postgres backup runs nightly with pg_dump; restore with pg_restore into a scratch database first."},
		{ID: "pg-upgrade", Text: "Upgrading Postgres from 15 to 16 needs pg_upgrade and a maintenance window."},
		{ID: "pg-pool", Text: "The connection pool for Postgres is PgBouncer in transaction mode."},
		{ID: "backup-laptop", Text: "Laptop backup goes to the shared drive every Friday."},
		{ID: "k8s-ingress", Text: "Kubernetes ingress uses NGINX; TLS certificates renew through cert-manager."},
		{ID: "k8s-quota", Text: "Each Kubernetes namespace gets 8 CPU cores."},
		{ID: "oncall-rotation", Text: "The on call rotation changes every Monday; the rotation calendar is in the wiki."},
		{ID: "oncall-pager", Text: "Pages go to whoever is on call, then to the team lead after 15 minutes."},
		{ID: "call-notes", Text: "Notes from the sales call: the customer is on the enterprise plan."},
		{ID: "slo-budget", Text: "The error budget of the checkout service is 0.1% per month."},
		{ID: "slo-reversed", Text: "Budget review: one error in the forecast spreadsheet."},
		{ID: "latency", Text: "The p99 latency of search is 450 ms at peak."},
		{ID: "the-filler", Text: "The team and the office and the lunch and the coffee."},
		{ID: "invoice-2024", Text: "Invoice INV-2024-117 from the hosting provider was paid in March."},
		{ID: "invoice-2025", Text: "Invoice INV-2025-003 is still open."},
		{ID: "cafe", Text: "Team lunch is at Café Lumière on Thursdays."},
		{ID: "tokyo", Text: "東京オフィスの住所は渋谷です。"},
		{ID: "kyoto", Text: "京都の会議は来週です。"},
		{ID: "pi", Text: "The ratio is 3.14 and the build number is 1,024."},
		{ID: "email", Text: "Escalations go to ops.lead@example.com, not the general inbox."},
		{ID: "recent-deploy", Text: "Deploy notes.", Time: at(20)},
	}
	queries := []RelevanceQuery{
		{
			Query: memory.Query{Text: `"blue green deployment"`}, Top: []string{"deploy-bg"},
			Absent: []string{"deploy-colours", "deploy-canary"}, Why: "an exact phrase; the same words apart do not match",
		},
		{
			Query: memory.Query{Text: `postgres backup`}, Top: []string{"pg-backup"},
			Why: "both words in one item beat one of them",
		},
		{
			Query: memory.Query{Text: `KUBERNETES`}, Top: []string{"k8s-quota", "k8s-ingress"},
			Why: "case folding; the shorter item first",
		},
		{
			Query: memory.Query{Text: `"don't deploy"`}, Top: []string{"deploy-friday"},
			Absent: []string{"deploy-checklist"}, Why: "an apostrophe inside a word",
		},
		{
			Query: memory.Query{Text: `"error budget"`}, Top: []string{"slo-budget"},
			Absent: []string{"slo-reversed"}, Why: "phrase order counts",
		},
		{
			Query: memory.Query{Text: `the latency`}, Top: []string{"latency"},
			Why: "a rare word outweighs a common one",
		},
		{
			Query: memory.Query{Text: `"on call" rotation`}, Top: []string{"oncall-rotation", "oncall-pager"},
			Absent: []string{"call-notes"}, Why: "a required phrase, and a word that only boosts",
		},
		{Query: memory.Query{Text: `café`}, Top: []string{"cafe"}, Why: "letters outside ASCII"},
		{
			Query: memory.Query{Text: `東京`}, Top: []string{"tokyo", "kyoto"},
			Why: "Han characters are a word each: both before one",
		},
		{
			Query: memory.Query{Text: `3.14 ops.lead`}, Top: []string{"email", "pi"},
			Absent: []string{"the-filler"}, Why: "numbers and dotted names stay one word",
		},
	}
	return Relevance{Mode: memory.ModeKeyword, Items: items, Queries: queries}
}

// semanticItems are the notes of SemanticRelevance: none shares a word with
// its query, so only meaning finds them. "travel-log" says "plane" five
// times among many other words: it outranks "aisle" for "flight
// preferences" only when vectors are not normalized.
func semanticItems() []memory.Item {
	return []memory.Item{
		{ID: "aisle", Text: "I always take an aisle seat on planes."},
		{ID: "travel-log", Text: "Travel log: the plane was late, the plane was full, the plane was cold, the plane landed late and the plane taxied for an hour."},
		{ID: "diet", Text: "She doesn't eat meat or fish."},
		{ID: "allergy", Text: "Peanuts give him a severe reaction."},
		{ID: "standup", Text: "The daily sync starts at nine thirty."},
		{ID: "laptop", Text: "His work computer is a ThinkPad X1."},
		{ID: "pay", Text: "Pay day is the last Friday of the month."},
		{ID: "dog", Text: "Their puppy is a golden retriever named Biscuit."},
		{ID: "car", Text: "Her automobile is a red hatchback."},
		{ID: "cancel", Text: "To end the subscription, write to billing two weeks before renewal."},
		{ID: "weather", Text: "It rained all week in Lisbon."},
		{ID: "garden", Text: "Tomatoes and basil grow on the balcony."},
	}
}

// semanticQueries are the paraphrase queries of SemanticRelevance.
func semanticQueries() []RelevanceQuery {
	q := func(text, top, why string) RelevanceQuery {
		return RelevanceQuery{Query: memory.Query{Text: text}, Top: []string{top}, Why: why}
	}
	return []RelevanceQuery{
		q("flight preferences", "aisle", "a paraphrase; a long text that repeats one related word comes after"),
		q("vegetarian diet", "diet", "a paraphrase"),
		q("nut allergy", "allergy", "a paraphrase"),
		q("morning standup time", "standup", "a paraphrase"),
		q("notebook brand", "laptop", "a paraphrase"),
		q("salary", "pay", "one word, a paraphrase"),
		q("pet dog breed", "dog", "a paraphrase"),
		q("car vehicle", "car", "a paraphrase"),
		q("cancel membership", "cancel", "a paraphrase"),
		q("portugal weather", "weather", "a paraphrase"),
		q("vegetable gardening", "garden", "a paraphrase"),
	}
}

// SemanticRelevance is the relevance set of ModeSemantic, with
// FakeEmbedder (DefaultFakeModel): 12 notes about people, and 11 queries
// that share no word with the note they must find first ("flight
// preferences" finds "I always take an aisle seat on planes."). A keyword
// search finds none of them.
func SemanticRelevance() Relevance {
	return Relevance{Mode: memory.ModeSemantic, Items: semanticItems(), Queries: semanticQueries()}
}

// HybridRelevance is the relevance set of ModeHybrid: the items of
// KeywordRelevance and SemanticRelevance together, the keyword queries
// (their top hit must stay first) and every paraphrase query (whose note
// must come first though no word matches). Absent IDs are not checked: a
// semantic ranking has every item.
func HybridRelevance() Relevance {
	kw := KeywordRelevance()
	set := Relevance{Mode: memory.ModeHybrid, Items: append(kw.Items, semanticItems()...)}
	for _, q := range kw.Queries {
		set.Queries = append(set.Queries, RelevanceQuery{Query: q.Query, Top: q.Top[:1], Why: q.Why + " (in hybrid, first)"})
	}
	set.Queries = append(set.Queries, semanticQueries()...)
	return set
}

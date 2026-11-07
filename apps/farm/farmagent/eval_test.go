//go:build eval

package farmagent_test

// The farm assistant on a real model (make farm-eval): the map, layout,
// handbook and web research tools against recorded map data and a fake
// search API, so each run asks the same questions of the same world.
// Needs ANTHROPIC_API_KEY or ANTHROPIC_KEY (FARM_EVAL_MODEL=anthropic,
// the default) or GEMINI_API_KEY or GEMINI_KEY (FARM_EVAL_MODEL=gemini).

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/elevation"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farmagent"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/handbook"
	"github.com/bsamanvith/agentkit/apps/farm/osm/osmtest"
	"github.com/bsamanvith/agentkit/apps/farm/planting"
	"github.com/bsamanvith/agentkit/apps/farm/research"
	"github.com/bsamanvith/agentkit/apps/farm/research/researchtest"
	"github.com/bsamanvith/agentkit/apps/farm/surveyor"
	credsqlite "github.com/bsamanvith/agentkit/pkg/credentials/sqlite"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/anthropic"
	"github.com/bsamanvith/agentkit/pkg/llm/gemini"
)

func evalModel(t *testing.T) llm.Model {
	t.Helper()
	env := func(keys ...string) string {
		for _, k := range keys {
			if v := os.Getenv(k); v != "" {
				return v
			}
		}
		return ""
	}
	switch p := env("FARM_EVAL_MODEL"); p {
	case "", "anthropic":
		key := env("ANTHROPIC_API_KEY", "ANTHROPIC_KEY")
		if key == "" {
			t.Skip("set ANTHROPIC_API_KEY or ANTHROPIC_KEY")
		}
		t.Setenv("ANTHROPIC_API_KEY", key)
		var opts []anthropic.Option
		if id := env("FARM_EVAL_MODEL_ID"); id != "" {
			opts = append(opts, anthropic.WithModel(id))
		}
		m, err := anthropic.New(opts...)
		if err != nil {
			t.Fatal(err)
		}
		return m
	case "gemini":
		key := env("GEMINI_API_KEY", "GEMINI_KEY")
		if key == "" {
			t.Skip("set GEMINI_API_KEY or GEMINI_KEY")
		}
		opts := []gemini.Option{gemini.WithAPIKey(key)}
		if id := env("FARM_EVAL_MODEL_ID"); id != "" {
			opts = append(opts, gemini.WithModel(id))
		}
		m, err := gemini.New(opts...)
		if err != nil {
			t.Fatal(err)
		}
		return m
	default:
		t.Fatalf("FARM_EVAL_MODEL %q: want anthropic or gemini", p)
		return nil
	}
}

type scenario struct {
	name string
	ask  string
	// One tool of each group must be called.
	tools [][]string
	// The reply must match each.
	says []*regexp.Regexp
	// And match none of these.
	never []*regexp.Regexp
	// connect connects Exa (to the fake) first.
	connect bool
	// group puts the plot in a group "Home farm" first.
	group bool
	// then checks the world afterwards.
	then func(t *testing.T, s *surveyor.Surveyor)
}

var (
	mapTools  = []string{farmagent.SurveyLandTool, farmagent.NearbyTool}
	bookTools = []string{"knowledge_search", "knowledge_outline", "knowledge_read"}
)

func scenarios() []scenario {
	re := regexp.MustCompile
	return []scenario{
		{
			name:  "proximity",
			ask:   "How far is my plot 'Paddy by the drain' from the nearest road and from water? What shape is it?",
			tools: [][]string{mapTools},
			says:  []*regexp.Regexp{re(`\b98\b`), re(`\b208\b`), re(`(?i)square`)},
		},
		{
			name:  "pond and shed, saved",
			ask:   "Lay out my plot 'Paddy by the drain' with a farm pond, a shed by the gate and a cart path, then save the layout if it checks out.",
			tools: [][]string{{farmagent.SuggestDesignTool}, {farmagent.SaveDesignTool}},
			says:  []*regexp.Regexp{re(`(?i)pond`)},
			then: func(t *testing.T, s *surveyor.Surveyor) {
				f, err := s.Farms().Get(context.Background(), "farm_k")
				if err != nil || f.Design == nil || len(f.Design.Elements) < 3 {
					t.Errorf("the layout was not saved: %+v %v", f.Design, err)
				}
			},
		},
		{
			name:  "telugu canal",
			ask:   "నా పొలం 'Paddy by the drain' నుండి కాలువ ఎంత దూరంలో ఉంది?",
			tools: [][]string{mapTools},
			says:  []*regexp.Regexp{re(`\b594\b|\b0\.6\b|\b590\b`), re(`\p{Telugu}`)},
		},
		{
			name:    "research seed prices",
			ask:     "Search the web: what paddy varieties suit the Godavari delta for kharif, and what does seed cost near Amalapuram?",
			connect: true,
			tools:   [][]string{{research.SearchTool}},
			says:    []*regexp.Regexp{re(`Delta-\d|డెల్టా-\d`), re(`(?i)(rs\.?|₹|rupees)\s?4[28]`)},
		},
		{
			name:  "research without a key",
			ask:   "Look up the current price of groundnut seed in Kurnool on the web.",
			tools: [][]string{{research.SearchTool}},
			says:  []*regexp.Regexp{re(`(?i)(connect|settings|key)`)},
			never: []*regexp.Regexp{re(`(?i)(rs\.?|₹)\s?\d{2,}`)},
		},
		{
			name:  "distribution and its shade",
			group: true,
			ask:   "In my group 'Home farm' I want a 5 m border of mango trees round the plot, and inside it vegetables (tomato with onion) and green gram, half and half. Lay it out: how many mango trees fit, and how much shade do they cast on the crops? Don't save anything yet.",
			tools: [][]string{{farmagent.DistributePlantsTool}},
			says:  []*regexp.Regexp{re(`(?i)shad`), re(`(?i)\b\d+\s+(mango|trees)`), re(`(?i)m²|m2|sq`)},
			then: func(t *testing.T, s *surveyor.Surveyor) {
				gs, err := s.Farms().Groups(context.Background())
				if err != nil || len(gs) != 1 || gs[0].Distribution != nil {
					t.Errorf("a distribution was saved without the farmer's yes: %+v %v", gs, err)
				}
			},
		},
		{
			name:  "rows north-south or east-west",
			group: true,
			ask:   "In 'Home farm' I'll grow sugarcane and groundnut in two equal strips. Should the strips run east to west or north to south? Compare both with the tool and tell me the difference in shade on the groundnut.",
			tools: [][]string{{farmagent.DistributePlantsTool}},
			says:  []*regexp.Regexp{re(`(?i)north.{0,4}south`), re(`(?i)shad`), re(`\d+\s?m²|\d+\s?m2|\d+\s?square`)},
		},
		{
			name:  "group the plots and save a layout",
			ask:   "Make a group called 'River fields' with my plot 'Paddy by the drain', then give it alternating 10 m strips of rice and green gram and save that.",
			tools: [][]string{{farmagent.SaveGroupTool}, {farmagent.SaveDistributionTool}},
			says:  []*regexp.Regexp{re(`(?i)river fields`)},
			then: func(t *testing.T, s *surveyor.Surveyor) {
				gs, err := s.Farms().Groups(context.Background())
				if err != nil || len(gs) != 1 || gs[0].Distribution == nil || gs[0].Distribution.Arrange != planting.Alternate || len(gs[0].Plots) != 1 {
					t.Errorf("the group and its distribution: %+v %v", gs, err)
				}
			},
		},
		{
			name:  "the noon sun and day length",
			ask:   "Where is the noon sun over my plot 'Paddy by the drain' in June and in December, and how long are the days then? Which side should tall crops go on?",
			tools: [][]string{{farmagent.GetSunTool}},
			says:  []*regexp.Regexp{re(`(?i)north`), re(`(?i)south`), re(`\b1[123]([.,]\d)?\s?(h|hours|hrs)`)},
		},
		{
			name:  "compare crops by month",
			ask:   "Compare rice, maize and chickpea on my plot 'Paddy by the drain': in which months is each best sown?",
			tools: [][]string{{farmagent.CompareCropsTool, farmagent.RecommendCropsTool}},
			says:  []*regexp.Regexp{re(`(?i)rice`), re(`(?i)chickpea`), re(`(?i)(june|july|october|november|december)`)},
		},
		{
			name:  "cocoa in the open sun",
			ask:   "Can I grow cocoa out in the open on 'Paddy by the drain'? What would it need?",
			tools: [][]string{{farmagent.RecommendCropsTool, farmagent.CompareCropsTool, farmagent.GetCropTool}},
			says:  []*regexp.Regexp{re(`(?i)shade`), re(`(?i)(coconut|arecanut|areca|shade net|trees)`)},
		},
		{
			name:  "border trees from the handbook",
			ask:   "Which trees make a good border on a paddy farm in coastal Andhra, and how far apart should I plant them? Use the handbook.",
			tools: [][]string{bookTools},
			says:  []*regexp.Regexp{re(`(?i)(neem|teak|drumstick|moringa|glyricidia|gliricidia|casuarina|subabul|coconut|palmyra|bamboo)`)},
		},
	}
}

func TestAssistantEval(t *testing.T) {
	model := evalModel(t)
	book, err := handbook.New(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	skills, err := farmagent.Skills()
	if err != nil {
		t.Fatal(err)
	}
	// Gemini's free tier allows a few model calls a minute: pause between
	// scenarios (FARM_EVAL_PAUSE, a duration).
	pause := time.Duration(0)
	if os.Getenv("FARM_EVAL_MODEL") == "gemini" {
		pause = time.Minute
	}
	if d, err := time.ParseDuration(os.Getenv("FARM_EVAL_PAUSE")); err == nil {
		pause = d
	}
	passed := 0
	for i, sc := range scenarios() {
		if i > 0 {
			time.Sleep(pause)
		}
		t.Run(sc.name, func(t *testing.T) {
			svc, _ := kothapeta(t)
			heights := elevation.Func(func(p geo.Point) float64 { return 14 + (p.Lat-16.70677)*3000 - (p.Lng-81.900394)*3000 })
			s := surveyor.New(svc, surveyor.WithFeatures(osmtest.Source()), surveyor.WithHeights(heights), surveyor.WithSearch(places{}, "IN"))
			fake := researchtest.New()
			defer fake.Close()
			store, err := credsqlite.Open(context.Background(), filepath.Join(t.TempDir(), "vault.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			vault, err := research.OpenVault(context.Background(), store, "", filepath.Join(t.TempDir(), "vault.key"))
			if err != nil {
				t.Fatal(err)
			}
			web, err := research.New(context.Background(), vault, research.WithBaseURLs(fake.URL, fake.URL))
			if err != nil {
				t.Fatal(err)
			}
			if sc.connect {
				if err := web.Connect(context.Background(), "exa", "exa-key-eval-1234"); err != nil {
					t.Fatal(err)
				}
			}
			webTools, err := web.Tools()
			if err != nil {
				t.Fatal(err)
			}
			if sc.group {
				if _, err := svc.CreateGroup(context.Background(), farm.GroupDraft{Name: "Home farm", Plots: []string{"farm_k"}}); err != nil {
					t.Fatal(err)
				}
			}
			a, err := farmagent.NewAssistant(model, svc, farmagent.WithSurveyor(s), farmagent.WithPlanner(plannerFor(t, svc)), farmagent.WithTools(webTools, research.System),
				farmagent.WithKnowledge(book, handbook.Description), farmagent.WithSkills(skills))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			r, err := a.Ask(ctx, "", sc.ask)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("tools %v\n%s", r.Tools, r.Text)
			ok := true
			for _, group := range sc.tools {
				if !slices.ContainsFunc(group, func(n string) bool { return slices.Contains(r.Tools, n) }) {
					t.Errorf("called none of %v (called %v)", group, r.Tools)
					ok = false
				}
			}
			for _, want := range sc.says {
				if !want.MatchString(r.Text) {
					t.Errorf("the reply does not say %s", want)
					ok = false
				}
			}
			for _, bad := range sc.never {
				if m := bad.FindString(r.Text); m != "" {
					t.Errorf("the reply says %q, which it cannot know", m)
					ok = false
				}
			}
			if sc.then != nil {
				sc.then(t, s)
			}
			if ok && !t.Failed() {
				passed++
			}
		})
	}
	t.Logf("%d of %d scenarios passed", passed, len(scenarios()))
}

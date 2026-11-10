// Command farm serves the farm app: its API under /api/ and the web app at
// /, from one process, on a SQLite file.
//
//	go run ./apps/farm/cmd/farm                       # http://127.0.0.1:8090
//	go run ./apps/farm/cmd/farm -addr :8090 -db farm.db -country IN
//
// The farm assistant runs on the first model of -models that answers
// (apps/farm/models): by default Claude, then Gemini, each when its key is
// set (ANTHROPIC_API_KEY or ANTHROPIC_KEY, GEMINI_API_KEY or GEMINI_KEY);
// OpenAI (OPENAI_API_KEY) and OpenRouter (OPENROUTER_API_KEY) can be listed
// too, and "off" turns the assistant off. A model is used only once it has
// passed make farm-eval, unless -models-unevaluated. Plots are looked up on
// OpenStreetMap's Nominatim unless -geocoder is "off"; the climate at a plot
// comes from NASA POWER unless -climate is "off"; what is around a plot
// from OpenStreetMap (Overpass, then the map API) unless -overpass and
// -osm-api are "off"; its slope from Open-Meteo's elevation API, or Open
// Topo Data when Open-Meteo cannot answer, unless -elevation and
// -elevation-backup are "off" (heights are kept in <db>-heights.json). The
// handbook is searched by meaning as well as
// keywords with Gemini embeddings when a Gemini key is set (-embedder).
//
// Web research uses each farmer's own Exa or Parallel key, saved in the
// app's settings and sealed in -vault (a SQLite file) with the key ring in
// FARM_CREDENTIALS_KEY, or a key file made beside it.
//
// Each plot's weather comes from Open-Meteo's forecast, or MET Norway's
// when Open-Meteo cannot answer (-weather lists the providers in order, or
// is "off"). Automations (-automations, on by default) run in the process:
// the weekly weather check, the heavy rain watch, the check of a new plan
// and the week's crop work, with notices in the app's inbox; with a model,
// also the assistant's skills (the weekly field brief, off until turned
// on). They keep their state in the -db file.
//
// Notices also go out by the message lanes (-notify, on with the
// automations): web push to the installed app is on from the start (its
// keys are made in <db>-push-keys.json); Telegram, email, WhatsApp and
// SMS each switch on when their keys are in the environment
// (FARM_TELEGRAM_TOKEN, FARM_SMTP_URL, FARM_WHATSAPP_TOKEN, FARM_MSG91_KEY
// or FARM_TWILIO_SID). -notify-routes says which lanes each kind of
// message goes by, -notify-caps bounds the paid ones. See
// apps/farm/docs/MESSAGES.md.
//
// The assistant's tools are also an MCP server at /mcp/ (unless -mcp is
// false): /mcp/farm (plots and planning), /mcp/farm-map (surveys, map
// questions, farm designs), /mcp/farm-research and /mcp/farm-handbook, so
// an agentkit agent (or any MCP client) can use them.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/automation"
	autosqlite "github.com/bsamanvith/agentkit/apps/farm/automation/sqlite"
	"github.com/bsamanvith/agentkit/apps/farm/elevation"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/sqlite"
	"github.com/bsamanvith/agentkit/apps/farm/farmagent"
	"github.com/bsamanvith/agentkit/apps/farm/farmapi"
	"github.com/bsamanvith/agentkit/apps/farm/feeds"
	"github.com/bsamanvith/agentkit/apps/farm/handbook"
	"github.com/bsamanvith/agentkit/apps/farm/models"
	"github.com/bsamanvith/agentkit/apps/farm/nasapower"
	"github.com/bsamanvith/agentkit/apps/farm/nominatim"
	"github.com/bsamanvith/agentkit/apps/farm/notify"
	"github.com/bsamanvith/agentkit/apps/farm/osm"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/prices"
	"github.com/bsamanvith/agentkit/apps/farm/prices/pricesqlite"
	"github.com/bsamanvith/agentkit/apps/farm/research"
	"github.com/bsamanvith/agentkit/apps/farm/surveyor"
	"github.com/bsamanvith/agentkit/apps/farm/watch"
	"github.com/bsamanvith/agentkit/apps/farm/weather"
	"github.com/bsamanvith/agentkit/apps/farm/webdist"
	credsqlite "github.com/bsamanvith/agentkit/pkg/credentials/sqlite"
	"github.com/bsamanvith/agentkit/pkg/llm"
	mcpserver "github.com/bsamanvith/agentkit/pkg/mcp/server"
	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/embed"
	"github.com/bsamanvith/agentkit/pkg/web"
)

// csp is pkg/web's policy plus the map tiles: OpenStreetMap's streets and
// Esri's World Imagery.
const csp = "default-src 'self'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' data: https://tile.openstreetmap.org https://server.arcgisonline.com; " +
	"font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; " +
	"form-action 'self'; frame-ancestors 'none'"

type config struct {
	addr, db, country, geocoder, climate, models                  string
	overpass, osmAPI, elevation, elevationBackup, embedder, vault string
	exaURL, parallelURL, weather                                  string
	priceProviders, priceFiles, dataGovURL                        string
	notifyRoutes, notifyCaps, publicURL                           string
	mcp, automations, unevaluated, notify                         bool
	embedRate, workers                                            int
}

// userAgent identifies the app to OpenStreetMap's servers, as their
// policies ask.
const userAgent = "agentkit-farm/0.1 (+https://github.com/bsamanvith/agentkit)"

func main() {
	var c config
	fs := flag.NewFlagSet("farm", flag.ExitOnError)
	fs.StringVar(&c.addr, "addr", env("FARM_ADDR", "127.0.0.1:8090"), "listen address ($FARM_ADDR)")
	fs.StringVar(&c.db, "db", env("FARM_DB", "farm.db"), "SQLite file ($FARM_DB)")
	fs.StringVar(&c.country, "country", env("FARM_COUNTRY", "IN"), "ISO code of the country plots should be in; empty for any ($FARM_COUNTRY)")
	fs.StringVar(&c.geocoder, "geocoder", env("FARM_GEOCODER", nominatim.DefaultURL), `Nominatim server, or "off" ($FARM_GEOCODER)`)
	fs.StringVar(&c.climate, "climate", env("FARM_CLIMATE", nasapower.DefaultURL), `NASA POWER server, or "off" ($FARM_CLIMATE)`)
	fs.StringVar(&c.models, "models", env("FARM_MODELS", defaultModels), `the assistant's models, tried in order, comma separated: provider/model-id, or a bare provider (anthropic, openai, gemini) for its default; openrouter/<vendor>/<model>; or "off" ($FARM_MODELS)`)
	fs.BoolVar(&c.unevaluated, "models-unevaluated", env("FARM_MODELS_UNEVALUATED", "false") == "true", "also use models that have not passed make farm-eval ($FARM_MODELS_UNEVALUATED)")
	fs.StringVar(&c.overpass, "overpass", env("FARM_OVERPASS", osm.DefaultOverpass), `Overpass servers, comma separated, or "off" ($FARM_OVERPASS)`)
	fs.StringVar(&c.osmAPI, "osm-api", env("FARM_OSM_API", osm.DefaultAPI), `OpenStreetMap API, the fallback for small boxes, or "off" ($FARM_OSM_API)`)
	fs.StringVar(&c.elevation, "elevation", env("FARM_ELEVATION", elevation.DefaultURL), `Open-Meteo elevation API, or "off" ($FARM_ELEVATION)`)
	fs.StringVar(&c.elevationBackup, "elevation-backup", env("FARM_ELEVATION_BACKUP", elevation.OpenTopoDataURL), `Open Topo Data API and dataset, asked when Open-Meteo cannot answer, or "off" ($FARM_ELEVATION_BACKUP)`)
	fs.StringVar(&c.embedder, "embedder", env("FARM_EMBEDDER", "auto"), `handbook search by meaning: auto (Gemini when its key is set), gemini or off ($FARM_EMBEDDER)`)
	fs.StringVar(&c.vault, "vault", env("FARM_VAULT", ""), "SQLite file of the farmers' sealed API keys (default: the -db file's name with -vault.db) ($FARM_VAULT)")
	fs.StringVar(&c.exaURL, "exa-url", env("FARM_EXA_URL", ""), "Exa API base URL (default Exa's) ($FARM_EXA_URL)")
	fs.StringVar(&c.parallelURL, "parallel-url", env("FARM_PARALLEL_URL", ""), "Parallel API base URL (default Parallel's) ($FARM_PARALLEL_URL)")
	fs.StringVar(&c.priceProviders, "prices", env("FARM_PRICE_PROVIDERS", "datagov,files"), `mandi price providers in the order they are asked, comma separated: datagov (Agmarknet on data.gov.in, key in $FARM_DATAGOV_KEY), files (the -price-files folder); "off" for saved prices only ($FARM_PRICE_PROVIDERS)`)
	fs.StringVar(&c.priceFiles, "price-files", env("FARM_PRICE_FILES", ""), "folder of downloaded price files (.csv from Agmarknet, data.gov.in or CEDA), read into the price history at start ($FARM_PRICE_FILES)")
	fs.StringVar(&c.dataGovURL, "datagov-url", env("FARM_DATAGOV_URL", prices.DataGovURL), "data.gov.in API base URL ($FARM_DATAGOV_URL)")
	fs.IntVar(&c.embedRate, "embed-rate", envInt("FARM_EMBED_RATE", 90), "texts a minute sent to the embedder, 0 for no cap (Gemini's free tier allows 100) ($FARM_EMBED_RATE)")
	fs.StringVar(&c.weather, "weather", env("FARM_WEATHER", "open-meteo,met-norway"), `forecast providers, first that answers: open-meteo and met-norway, each optionally =URL; or "off" ($FARM_WEATHER)`)
	fs.BoolVar(&c.automations, "automations", env("FARM_AUTOMATIONS", "true") == "true", "run the automations: weather checks, crop work, skills ($FARM_AUTOMATIONS)")
	fs.IntVar(&c.workers, "automation-workers", envInt("FARM_AUTOMATION_WORKERS", 4), "automation jobs run at once ($FARM_AUTOMATION_WORKERS)")
	fs.BoolVar(&c.notify, "notify", env("FARM_NOTIFY", "true") == "true", "send automations' notices beyond the inbox, by the message lanes: web push, Telegram, email, WhatsApp, SMS, each on when its keys are set ($FARM_NOTIFY)")
	fs.StringVar(&c.notifyRoutes, "notify-routes", env("FARM_NOTIFY_ROUTES", notify.DefaultRoutes), "which lanes each kind and severity of message goes by: key=tree; ... with fanout(...) and chain(...) ($FARM_NOTIFY_ROUTES)")
	fs.StringVar(&c.notifyCaps, "notify-caps", env("FARM_NOTIFY_CAPS", DefaultCaps), "paid lanes' cost per message (paise) and caps a day and a month: lane:cost=14,day=300,month=5000;... ($FARM_NOTIFY_CAPS)")
	fs.StringVar(&c.publicURL, "public-url", env("FARM_PUBLIC_URL", ""), "the app's public URL, for links in messages, email unsubscribe links and the lanes' webhooks at /hooks/ ($FARM_PUBLIC_URL)")
	fs.BoolVar(&c.mcp, "mcp", env("FARM_MCP", "true") == "true", "serve the assistant's tools over MCP at /mcp/ ($FARM_MCP)")
	_ = fs.Parse(os.Args[1:])

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, c, os.Stderr)
	stop()
	if err != nil {
		slog.Error("farm", "err", err)
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func run(ctx context.Context, c config, logw io.Writer) error {
	log := slog.New(slog.NewTextHandler(logw, nil))
	store, err := sqlite.Open(ctx, c.db)
	if err != nil {
		return err
	}
	defer store.Close()

	// The engine hears of each saved change to a farm (a new plan fires
	// its weather check); it is set below, before the server starts.
	var engine *automation.Engine
	observe := farm.WithObserver(func(_ context.Context, e farm.Event) {
		if engine != nil {
			engine.Emit(e.Name, e.Farm)
		}
	})
	vopts := []farm.VerifierOption{farm.WithCountry(c.country)}
	if c.geocoder != "off" {
		loc, err := nominatim.New("agentkit-farm/0.1 (+https://github.com/bsamanvith/agentkit)", nominatim.WithURL(c.geocoder))
		if err != nil {
			return err
		}
		vopts = append(vopts, farm.WithLocator(loc))
	}
	svc, err := farm.NewService(store, farm.NewVerifier(vopts...), observe)
	if err != nil {
		return err
	}
	lib, err := agro.Default()
	if err != nil {
		return err
	}
	var climate agro.ClimateSource
	if c.climate != "off" {
		if climate, err = nasapower.New(nasapower.WithURL(c.climate)); err != nil {
			return err
		}
	}
	plan := planner.New(svc, lib, climate)
	sopts := []surveyor.Option{surveyor.WithLogger(log)}
	var sources []osm.Source
	if c.overpass != "off" && c.overpass != "" {
		src, err := osm.Overpass(userAgent, strings.Split(c.overpass, ","))
		if err != nil {
			return err
		}
		sources = append(sources, src)
	}
	if c.osmAPI != "off" && c.osmAPI != "" {
		src, err := osm.API(userAgent, c.osmAPI)
		if err != nil {
			return err
		}
		sources = append(sources, src)
	}
	if len(sources) > 0 {
		sopts = append(sopts, surveyor.WithFeatures(osm.Chain(sources...)))
	}
	if hs, err := buildHeights(c, log); err != nil {
		return err
	} else if hs != nil {
		sopts = append(sopts, surveyor.WithHeights(hs))
	}
	if c.geocoder != "off" {
		search, err := nominatim.New(userAgent, nominatim.WithURL(c.geocoder))
		if err != nil {
			return err
		}
		sopts = append(sopts, surveyor.WithSearch(search, c.country))
	}
	survey := surveyor.New(svc, sopts...)

	vaultPath := c.vault
	if vaultPath == "" {
		vaultPath = strings.TrimSuffix(c.db, filepath.Ext(c.db)) + "-vault.db"
	}
	vstore, err := credsqlite.Open(ctx, vaultPath)
	if err != nil {
		return err
	}
	defer vstore.Close()
	vault, err := research.OpenVault(ctx, vstore, os.Getenv("FARM_CREDENTIALS_KEY"), vaultPath+".key")
	if err != nil {
		return err
	}
	webResearch, err := research.New(ctx, vault, research.WithBaseURLs(c.exaURL, c.parallelURL))
	if err != nil {
		return err
	}
	webTools, err := webResearch.Tools()
	if err != nil {
		return err
	}

	priceDB, err := pricesqlite.Open(ctx, strings.TrimSuffix(c.db, filepath.Ext(c.db))+"-prices.db")
	if err != nil {
		return err
	}
	defer priceDB.Close()
	market, err := buildPrices(ctx, c, priceDB, log)
	if err != nil {
		return err
	}

	emb, err := buildEmbedder(c)
	if err != nil {
		return err
	}
	var hopts []handbook.Option
	var vectors *handbook.Vectors
	if emb != nil {
		// The handbook's vectors are kept beside the database: the first
		// start asks for them (paced for a free-tier key, a minute or two),
		// later starts read the file.
		vectors = handbook.NewVectors(emb, strings.TrimSuffix(c.db, filepath.Ext(c.db))+"-vectors.gob", c.embedRate)
		log.Info("farm: indexing the handbook", "remembered_vectors", vectors.Len())
		hopts = append(hopts, handbook.WithEmbedder(vectors))
	}
	book, err := handbook.New(ctx, lib, hopts...)
	if vectors != nil {
		if serr := vectors.Save(); serr != nil {
			log.Warn("farm: saving the handbook's vectors", "err", serr)
		}
	}
	if err != nil {
		if emb == nil {
			return err
		}
		// The embedding API failed: search by keyword rather than not start.
		log.Warn("farm: handbook embeddings failed; keyword search only", "err", err)
		if book, err = handbook.New(ctx, lib); err != nil {
			return err
		}
	}
	skills, err := farmagent.Skills()
	if err != nil {
		return err
	}
	forecast, err := buildWeather(c.weather, log)
	if err != nil {
		return err
	}
	watcher := watch.New(plan, forecast)
	var astore *autosqlite.Store
	if c.automations {
		if astore, err = autosqlite.Open(ctx, c.db); err != nil {
			return err
		}
		defer astore.Close()
	}
	// The MCP server serves these tools; the assistant also has its skills
	// (an agent elsewhere brings its own).
	toolOpts := []farmagent.Option{
		farmagent.WithPlanner(plan), farmagent.WithSurveyor(survey),
		farmagent.WithTools(webTools, research.System),
		farmagent.WithKnowledge(book, handbook.Description), farmagent.WithPrices(market),
	}
	var alerts automation.Store // a nil *autosqlite.Store is not a nil Store
	if astore != nil {
		alerts = astore
	}
	toolOpts = append(toolOpts, farmagent.WithWatch(watcher, alerts))
	agentOpts := append(slices.Clone(toolOpts), farmagent.WithSkills(skills))

	aopts := []farmapi.Option{
		farmapi.WithCountry(c.country), farmapi.WithLogger(log), farmapi.WithPlanner(plan),
		farmapi.WithSurveyor(survey), farmapi.WithResearch(webResearch), farmapi.WithWatch(watcher), farmapi.WithPrices(market),
	}
	model, provider, err := buildModel(c, log)
	if err != nil {
		return err
	}
	if model != nil {
		a, err := farmagent.NewAssistant(model, svc, agentOpts...)
		if err != nil {
			return err
		}
		aopts = append(aopts, farmapi.WithAssistant(a))
	}
	var hooks map[string]http.Handler // the message lanes' webhooks
	if astore != nil {
		eopts := append(watcher.Rules(), automation.WithLogger(log), automation.WithWorkers(c.workers))
		if forecast != nil {
			eopts = append(eopts, automation.WithWeather(forecast))
		}
		if model != nil {
			runner, err := farmagent.NewSkillRunner(model, svc, agentOpts...)
			if err != nil {
				return err
			}
			eopts = append(eopts, automation.WithSkills(runner))
		}
		if c.notify {
			msgs, err := buildNotify(ctx, c, log)
			if err != nil {
				return err
			}
			defer msgs.store.Close()
			link := func(farm string) string {
				if c.publicURL == "" {
					return ""
				}
				return strings.TrimRight(c.publicURL, "/") + "/#/farms/" + url.PathEscape(farm)
			}
			eopts = append(eopts, automation.WithChannels(watch.Messages(msgs.hub.Dispatcher, link)))
			aopts = append(aopts, farmapi.WithNotify(msgs.hub))
			hooks = msgs.hooks
			for _, f := range msgs.run {
				go f(ctx)
			}
		}
		e, err := automation.New(astore, watcher, eopts...)
		if err != nil {
			return err
		}
		if err := e.Seed(ctx, watch.Builtins(forecast != nil, model != nil)); err != nil {
			return err
		}
		engine = e
		aopts = append(aopts, farmapi.WithAutomations(e))
		go func() {
			if err := e.Run(ctx); err != nil {
				log.Error("farm: automations", "err", err)
			}
		}()
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", farmapi.New(svc, aopts...))
	for path, h := range hooks {
		mux.Handle(path, h)
	}
	if c.mcp {
		sets, err := farmagent.Toolsets(svc, toolOpts...)
		if err != nil {
			return err
		}
		srv, err := mcpserver.New(sets, mcpserver.WithBasePath("/mcp"), mcpserver.WithLogger(log))
		if err != nil {
			return err
		}
		mux.Handle("/mcp/", srv)
		mux.Handle("/mcp", srv)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	mux.Handle("/", web.New(webdist.FS(), web.WithCSP(csp)).Handler())

	ln, err := net.Listen("tcp", c.addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Info("farm: serving", "url", "http://"+ln.Addr().String(), "db", c.db, "assistant", provider, "geocoder", c.geocoder,
		"climate", c.climate, "weather", c.weather, "automations", c.automations, "overpass", c.overpass, "osm_api", c.osmAPI, "elevation", c.elevation, "elevation_backup", c.elevationBackup, "embedder", emb != nil, "prices", strings.Join(market.Providers(), ", then "), "notify", c.notify && astore != nil, "mcp", c.mcp)
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shut); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// buildPrices returns the price service: the live providers c names,
// in order, then the saved history in h. Price files are read into the
// history in the background, so a large folder does not hold up the start.
func buildPrices(ctx context.Context, c config, h prices.History, log *slog.Logger) (*prices.Service, error) {
	var files *prices.FileSource
	makers := map[string]func() (prices.Source, error){
		"datagov": func() (prices.Source, error) {
			return prices.DataGov(os.Getenv("FARM_DATAGOV_KEY"), prices.WithDataGovURL(c.dataGovURL))
		},
		"files": func() (prices.Source, error) {
			f, err := prices.Files(c.priceFiles)
			if err == nil {
				files = f
			}
			return f, err
		},
	}
	live, err := feeds.Pick(log, c.priceProviders, makers)
	if err != nil {
		return nil, err
	}
	if files == nil && c.priceFiles != "" {
		// Files to import, though not asked as a live provider.
		if files, err = prices.Files(c.priceFiles); err != nil {
			return nil, err
		}
	}
	opts := []prices.Option{prices.WithLogger(log)}
	if files != nil {
		opts = append(opts, prices.WithFiles(files))
	}
	svc := prices.New(h, live, opts...)
	if files != nil {
		go func() {
			stats, n, err := svc.Import(ctx)
			if err != nil {
				log.Warn("farm: reading price files", "err", err)
				return
			}
			log.Info("farm: price files read", "files", stats.Files, "quotes", stats.Quotes, "new", n, "skipped", stats.Skipped, "unreadable", stats.Errors)
		}()
	}
	return svc, nil
}

// defaultModels is the lane without -models: Claude, then Gemini.
const defaultModels = "anthropic,gemini"

// buildModel returns the assistant's model lane, or nil when -models is
// "off" or no listed model is usable, and logs where each model stands.
func buildModel(c config, log *slog.Logger) (llm.Model, string, error) {
	if c.models == "off" {
		return nil, "off", nil
	}
	names, err := models.Parse(c.models)
	if err != nil {
		return nil, "", fmt.Errorf("-models: %w", err)
	}
	opts := []models.Option{models.WithLogger(log)}
	if c.unevaluated {
		opts = append(opts, models.AllowUnevaluated())
	}
	lane := models.New(names, opts...)
	for _, s := range lane.Statuses() {
		log.Info("farm: model", "model", s.Model, "state", s.State, "cost", s.Cost, "why", s.Reason)
	}
	if lane.Len() == 0 {
		return nil, "off", nil
	}
	return lane, lane.Active(), nil
}

// buildEmbedder returns the handbook's embedder: Gemini's embeddings
// through its OpenAI-compatible endpoint, or nil.
func buildEmbedder(c config) (memory.Embedder, error) {
	key := firstEnv("GEMINI_API_KEY", "GEMINI_KEY")
	switch c.embedder {
	case "off":
		return nil, nil
	case "auto":
		if key == "" {
			return nil, nil
		}
	case "gemini":
		if key == "" {
			return nil, errors.New("-embedder gemini needs GEMINI_API_KEY or GEMINI_KEY")
		}
	default:
		return nil, fmt.Errorf("-embedder %q: want auto, gemini or off", c.embedder)
	}
	return embed.NewOpenAI(embed.Config{
		Model: "gemini-embedding-001", Dimensions: 3072, Key: key,
		BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
		// Gemini takes at most 100 texts a request.
		BatchSize: 100,
	})
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// buildHeights is Open-Meteo, then Open Topo Data, sharing one cache kept
// beside the database (nil when both are off).
func buildHeights(c config, log *slog.Logger) (elevation.Source, error) {
	cache, err := elevation.OpenCache(strings.TrimSuffix(c.db, filepath.Ext(c.db)) + "-heights.json")
	if err != nil {
		return nil, err
	}
	var srcs []elevation.Source
	if c.elevation != "off" && c.elevation != "" {
		h, err := elevation.OpenMeteo(c.elevation, elevation.WithCache(cache))
		if err != nil {
			return nil, err
		}
		srcs = append(srcs, h)
	}
	if c.elevationBackup != "off" && c.elevationBackup != "" {
		h, err := elevation.OpenTopoData(c.elevationBackup, elevation.WithCache(cache))
		if err != nil {
			return nil, err
		}
		srcs = append(srcs, h)
	}
	if len(srcs) == 0 {
		return nil, nil
	}
	return elevation.Fallback(log, srcs...), nil
}

// buildWeather is the one place the forecast providers are picked: a
// comma-separated list of names, each optionally =URL, asked in order.
// A new provider is a weather.Source and a case here.
func buildWeather(list string, log *slog.Logger) (weather.Source, error) {
	if list == "off" || list == "" {
		return nil, nil
	}
	var srcs []weather.Source
	for _, item := range strings.Split(list, ",") {
		name, u, _ := strings.Cut(strings.TrimSpace(item), "=")
		switch name {
		case "open-meteo":
			s, err := weather.OpenMeteo(u)
			if err != nil {
				return nil, err
			}
			srcs = append(srcs, s)
		case "met-norway":
			s, err := weather.NewMETNorway(u, userAgent)
			if err != nil {
				return nil, err
			}
			srcs = append(srcs, s)
		default:
			return nil, fmt.Errorf("-weather: unknown provider %q: want open-meteo or met-norway", name)
		}
	}
	return weather.Fallback(log, srcs...), nil
}

// Command farm serves the farm app: its API under /api/ and the web app at
// /, from one process, on a SQLite file.
//
//	go run ./apps/farm/cmd/farm                       # http://127.0.0.1:8090
//	go run ./apps/farm/cmd/farm -addr :8090 -db farm.db -country IN
//
// The farm assistant runs on Claude when ANTHROPIC_API_KEY (or
// ANTHROPIC_KEY) is set, else on Gemini with GEMINI_API_KEY (or
// GEMINI_KEY); -model picks one, or "off". Plots are looked up on
// OpenStreetMap's Nominatim unless -geocoder is "off"; the climate at a plot
// comes from NASA POWER unless -climate is "off".
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
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/sqlite"
	"github.com/bsamanvith/agentkit/apps/farm/farmagent"
	"github.com/bsamanvith/agentkit/apps/farm/farmapi"
	"github.com/bsamanvith/agentkit/apps/farm/handbook"
	"github.com/bsamanvith/agentkit/apps/farm/nasapower"
	"github.com/bsamanvith/agentkit/apps/farm/nominatim"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/webdist"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/anthropic"
	"github.com/bsamanvith/agentkit/pkg/llm/gemini"
	"github.com/bsamanvith/agentkit/pkg/web"
)

// csp is pkg/web's policy plus the map tiles: OpenStreetMap's streets and
// Esri's World Imagery.
const csp = "default-src 'self'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' data: https://tile.openstreetmap.org https://server.arcgisonline.com; " +
	"font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; " +
	"form-action 'self'; frame-ancestors 'none'"

type config struct {
	addr, db, country, geocoder, climate, model, modelID string
}

func main() {
	var c config
	fs := flag.NewFlagSet("farm", flag.ExitOnError)
	fs.StringVar(&c.addr, "addr", env("FARM_ADDR", "127.0.0.1:8090"), "listen address ($FARM_ADDR)")
	fs.StringVar(&c.db, "db", env("FARM_DB", "farm.db"), "SQLite file ($FARM_DB)")
	fs.StringVar(&c.country, "country", env("FARM_COUNTRY", "IN"), "ISO code of the country plots should be in; empty for any ($FARM_COUNTRY)")
	fs.StringVar(&c.geocoder, "geocoder", env("FARM_GEOCODER", nominatim.DefaultURL), `Nominatim server, or "off" ($FARM_GEOCODER)`)
	fs.StringVar(&c.climate, "climate", env("FARM_CLIMATE", nasapower.DefaultURL), `NASA POWER server, or "off" ($FARM_CLIMATE)`)
	fs.StringVar(&c.model, "model", env("FARM_MODEL", "auto"), `assistant's provider: auto, anthropic, gemini or off ($FARM_MODEL)`)
	fs.StringVar(&c.modelID, "model-id", env("FARM_MODEL_ID", ""), "model ID (default: the provider's default) ($FARM_MODEL_ID)")
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

	vopts := []farm.VerifierOption{farm.WithCountry(c.country)}
	if c.geocoder != "off" {
		loc, err := nominatim.New("agentkit-farm/0.1 (+https://github.com/bsamanvith/agentkit)", nominatim.WithURL(c.geocoder))
		if err != nil {
			return err
		}
		vopts = append(vopts, farm.WithLocator(loc))
	}
	svc, err := farm.NewService(store, farm.NewVerifier(vopts...))
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
	aopts := []farmapi.Option{farmapi.WithCountry(c.country), farmapi.WithLogger(log), farmapi.WithPlanner(plan)}
	model, provider, err := buildModel(c)
	if err != nil {
		return err
	}
	if model != nil {
		book, err := handbook.New(ctx)
		if err != nil {
			return err
		}
		skills, err := farmagent.Skills()
		if err != nil {
			return err
		}
		a, err := farmagent.NewAssistant(model, svc, farmagent.WithPlanner(plan),
			farmagent.WithKnowledge(book, handbook.Description), farmagent.WithSkills(skills))
		if err != nil {
			return err
		}
		aopts = append(aopts, farmapi.WithAssistant(a))
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", farmapi.New(svc, aopts...))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	mux.Handle("/", web.New(webdist.FS(), web.WithCSP(csp)).Handler())

	ln, err := net.Listen("tcp", c.addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Info("farm: serving", "url", "http://"+ln.Addr().String(), "db", c.db, "assistant", provider, "geocoder", c.geocoder, "climate", c.climate)
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

// buildModel returns the assistant's model and its provider's name, or nil
// and "off" when there is no key or c.model is "off".
func buildModel(c config) (llm.Model, string, error) {
	claudeKey := firstEnv("ANTHROPIC_API_KEY", "ANTHROPIC_KEY")
	geminiKey := firstEnv("GEMINI_API_KEY", "GEMINI_KEY")
	p := c.model
	if p == "auto" {
		switch {
		case claudeKey != "":
			p = "anthropic"
		case geminiKey != "":
			p = "gemini"
		default:
			p = "off"
		}
	}
	switch p {
	case "off":
		return nil, "off", nil
	case "anthropic":
		opts := []anthropic.Option{}
		if c.modelID != "" {
			opts = append(opts, anthropic.WithModel(c.modelID))
		}
		if os.Getenv("ANTHROPIC_API_KEY") == "" && claudeKey != "" {
			// The SDK reads ANTHROPIC_API_KEY; Claude sessions set ANTHROPIC_KEY.
			if err := os.Setenv("ANTHROPIC_API_KEY", claudeKey); err != nil {
				return nil, "", err
			}
		}
		m, err := anthropic.New(opts...)
		return m, p, err
	case "gemini":
		opts := []gemini.Option{gemini.WithAPIKey(geminiKey)}
		if c.modelID != "" {
			opts = append(opts, gemini.WithModel(c.modelID))
		}
		m, err := gemini.New(opts...)
		return m, p, err
	}
	return nil, "", fmt.Errorf("-model %q: want auto, anthropic, gemini or off", c.model)
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

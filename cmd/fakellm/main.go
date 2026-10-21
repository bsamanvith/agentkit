// Command fakellm serves a scripted stand-in for the Anthropic Messages API
// (internal/fakellm), for local runs and end-to-end tests that need a model
// but no key. Point a client at it with ANTHROPIC_BASE_URL.
//
//	fakellm [serve]      serve the scripts (the image's default command)
//	fakellm healthcheck  exit 0 if a fakellm on this host answers /healthz
//	fakellm version      print the version
//
// Environment:
//
//	FAKELLM_SCRIPT_DIR   directory of name.jsonl scripts (default deploy/local/fakellm)
//	FAKELLM_SCRIPT       script for requests without X-Fakellm-Script (default "default")
//	FAKELLM_ADDR         listen address; else AGENTKIT_ADDR, which the shared
//	                     image sets; else 127.0.0.1:8090
//
// It is a development and test tool: it never goes into the agentkit binary
// or its images.
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/bsamanvith/agentkit/internal/fakellm"
)

// Set by the image build (-X main.version=...).
var (
	version = "dev"
	commit  = ""
	date    = ""
)

const (
	envDir    = "FAKELLM_SCRIPT_DIR"
	envScript = "FAKELLM_SCRIPT"
	envAddr   = "FAKELLM_ADDR"
	envShared = "AGENTKIT_ADDR"

	defaultDir    = "deploy/local/fakellm"
	defaultScript = "default"
	defaultAddr   = "127.0.0.1:8090"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr, nil)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakellm:", err)
		if errors.Is(err, errUsage) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

var errUsage = errors.New("usage: fakellm [serve | healthcheck | version]")

// run runs one command. ready, if not nil, is called with the address once
// serve listens.
func run(ctx context.Context, args []string, env func(string) string, stdout, stderr io.Writer, ready func(addr string)) error {
	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	if len(args) > 0 {
		return errUsage
	}
	switch cmd {
	case "serve":
		return serve(ctx, env, stderr, ready)
	case "healthcheck":
		return healthcheck(ctx, addr(env))
	case "version":
		_, err := fmt.Fprintf(stdout, "fakellm %s %s %s\n", version, commit, date)
		return err
	default:
		return errUsage
	}
}

func addr(env func(string) string) string {
	return cmp.Or(env(envAddr), env(envShared), defaultAddr)
}

func serve(ctx context.Context, env func(string) string, stderr io.Writer, ready func(string)) error {
	log := slog.New(slog.NewTextHandler(stderr, nil))
	dir := cmp.Or(env(envDir), defaultDir)
	scripts, err := fakellm.LoadDir(dir)
	if err != nil {
		return fmt.Errorf("%w (set %s)", err, envDir)
	}
	def := cmp.Or(env(envScript), defaultScript)
	h, err := fakellm.New(scripts, fakellm.WithDefault(def), fakellm.WithLogger(log))
	if err != nil {
		return fmt.Errorf("%w (set %s)", err, envScript)
	}
	ln, err := net.Listen("tcp", addr(env))
	if err != nil {
		return err
	}
	names := slices.Sorted(maps.Keys(scripts))
	log.Info("fakellm listening", "addr", ln.Addr().String(), "dir", dir, "default", def, "scripts", strings.Join(names, ","), "version", version)
	if ready != nil {
		ready(ln.Addr().String())
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// healthcheck asks a fakellm listening on addr for /healthz, so a container
// without a shell can check itself (the image's HEALTHCHECK).
func healthcheck(ctx context.Context, listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	url := "http://" + net.JoinHostPort(host, port) + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return nil
}

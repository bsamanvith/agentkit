package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// defaultHealthTimeout bounds one healthcheck, from dialing to the status
// line. Keep it below the probe's own timeout (the image's HEALTHCHECK: 3s).
const defaultHealthTimeout = 2 * time.Second

// healthcheck asks a running agentkit serve on $AGENTKIT_ADDR whether it is
// live (GET /healthz) or, with -ready, ready (GET /readyz), and returns an
// error unless the answer is 200. It is how a container without a shell
// checks itself: it prints nothing on success and one line on failure.
func (c cli) healthcheck(ctx context.Context, args []string) error {
	fs := c.newFlagSet("agentkit healthcheck", "")
	ready := fs.Bool("ready", false, "check readiness (/readyz) instead of liveness (/healthz)")
	timeout := fs.Duration("timeout", defaultHealthTimeout, "give up after this long")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *timeout <= 0 {
		fs.Usage()
		return errUsage
	}
	addr := c.env(envAddr)
	if addr == "" {
		addr = "127.0.0.1:8080" // serve's default
	}
	target, err := dialAddr(addr)
	if err != nil {
		return fmt.Errorf("%s: %w", envAddr, err)
	}
	path := "/healthz"
	if *ready {
		path = "/readyz"
	}

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+target+path, nil)
	if err != nil {
		return err
	}
	// No proxy: the server is on this host, whatever HTTP_PROXY says.
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("GET %s: no answer within %s", req.URL, *timeout)
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", req.URL, resp.Status)
	}
	return nil
}

// dialAddr turns a listen address into one to connect to: a server listening
// on every interface (":8080", "0.0.0.0:8080", "[::]:8080") is reached on
// loopback.
func dialAddr(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", err
	}
	if port == "" || port == "0" {
		return "", fmt.Errorf("%q has no fixed port to check", listen)
	}
	switch ip := net.ParseIP(host); {
	case host == "", ip != nil && ip.To4() != nil && ip.IsUnspecified():
		host = "127.0.0.1"
	case ip != nil && ip.IsUnspecified():
		host = "::1"
	}
	return net.JoinHostPort(host, port), nil
}

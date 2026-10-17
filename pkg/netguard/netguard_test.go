package netguard_test

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/netguard"
)

func TestReason(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.255.0.9", "10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.1.1",
		"169.254.169.254", "169.254.0.1", "100.64.0.1", "100.127.255.255", "0.0.0.0", "0.1.2.3",
		"224.0.0.1", "255.255.255.255", "240.0.0.1", "192.0.2.1", "198.18.0.1", "192.0.0.170",
		"::", "::1", "fe80::1", "fc00::1", "fd00:ec2::254", "ff02::1", "2001:db8::1", "fec0::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254", "::ffff:100.64.0.1", "::ffff:192.0.2.1",
		"2002:7f00:0001::", "2002:a9fe:a9fe::1", // 6to4 of 127.0.0.1 and 169.254.169.254
		"64:ff9b::7f00:1", "64:ff9b::a00:1", "64:ff9b:1::a00:1", // NAT64 of 127.0.0.1 and 10.0.0.1
		"2001::1", "100::1",
	}
	for _, s := range blocked {
		if netguard.Reason(netip.MustParseAddr(s)) == "" {
			t.Errorf("%s is allowed", s)
		}
	}
	if netguard.Reason(netip.MustParseAddr("2606:4700::1%eth0")) == "" {
		t.Error("a zoned address is allowed")
	}
	for _, s := range []string{"1.1.1.1", "8.8.8.8", "93.184.216.34", "172.32.0.1", "100.128.0.1", "2606:4700:4700::1111", "::ffff:8.8.8.8", "2002:0808:0808::", "64:ff9b::808:808"} {
		if why := netguard.Reason(netip.MustParseAddr(s)); why != "" {
			t.Errorf("%s is refused: %s", s, why)
		}
	}
}

func TestCheckURL(t *testing.T) {
	g, err := netguard.New([]string{"mcp.internal", "*.corp.example"})
	if err != nil {
		t.Fatal(err)
	}
	for _, ok := range []string{
		"https://mcp.example.com/mcp", "https://mcp.example.com:8443/x?y=1", "https://8.8.8.8/mcp",
		"http://mcp.internal/mcp", "https://MCP.Internal./mcp", "http://a.b.corp.example/x", "https://10.0.0.1.nip.io/",
	} {
		if _, err := g.CheckURL(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"http://mcp.example.com/", "ftp://mcp.example.com/", "https:///path", "https://user:pw@mcp.example.com/",
		"https://127.0.0.1/", "https://[::1]/", "https://[::ffff:127.0.0.1]/", "https://169.254.169.254/latest/meta-data",
		"https://10.0.0.1/", "http://corp.example/", "%zz", "mcp.example.com",
	} {
		if _, err := g.CheckURL(bad); !errors.Is(err, netguard.ErrBlocked) {
			t.Errorf("%s: %v, want ErrBlocked", bad, err)
		}
	}
	for _, h := range []string{"", "*", "*.", "a/b", "host:80", ".x", "x.", "*.*.x"} {
		if _, err := netguard.New([]string{h}); err == nil {
			t.Errorf("allowed host %q accepted", h)
		}
	}
}

// fakeDNS answers every A query with ip and every other query with nothing.
func fakeDNS(t *testing.T, ip netip.Addr) *net.Resolver {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := buf[:n]
			// The question: name labels, then type and class.
			i := 12
			for i < n && q[i] != 0 {
				i += int(q[i]) + 1
			}
			if i+5 > n {
				continue
			}
			qtype := binary.BigEndian.Uint16(q[i+1:])
			resp := append([]byte(nil), q[:i+5]...)
			resp[2], resp[3] = 0x81, 0x80 // response, recursion available, no error
			resp[6], resp[7] = 0, 0       // answers
			if qtype == 1 {
				resp[7] = 1
				a4 := ip.As4()
				resp = append(resp, 0xc0, 12, 0, 1, 0, 1, 0, 0, 0, 0, 0, 4)
				resp = append(resp, a4[:]...)
			}
			_, _ = pc.WriteTo(resp, from)
		}
	}()
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "udp", pc.LocalAddr().String())
	}}
}

// server is an https server on 127.0.0.1 that counts its requests.
func server(t *testing.T, h http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s, &n
}

func get(t *testing.T, g *netguard.Guard, s *httptest.Server, url string) (string, error) {
	t.Helper()
	resp, err := g.Client(s.Client().Transport.(*http.Transport)).Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

// A name resolving to an internal address gets no connection, however the
// URL looked when it was checked; an allowed host connects.
func TestDial(t *testing.T) {
	s, hits := server(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	port := s.URL[strings.LastIndex(s.URL, ":"):]
	// example.com is in httptest's certificate; the fake DNS sends it to
	// 127.0.0.1, as a rebinding name would.
	r := fakeDNS(t, netip.MustParseAddr("127.0.0.1"))

	g, _ := netguard.New(nil, netguard.WithResolver(r))
	if _, err := g.CheckURL("https://example.com" + port); err != nil {
		t.Fatalf("the URL alone is fine: %v", err)
	}
	for _, u := range []string{"https://example.com" + port, s.URL, "https://[::ffff:127.0.0.1]" + port, "https://localhost" + port} {
		if _, err := get(t, g, s, u); !errors.Is(err, netguard.ErrBlocked) {
			t.Errorf("%s: %v, want ErrBlocked", u, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("the server got %d requests", hits.Load())
	}

	allow, _ := netguard.New([]string{"example.com"}, netguard.WithResolver(r))
	if body, err := get(t, allow, s, "https://example.com"+port); err != nil || body != "ok" {
		t.Fatalf("allowed host: %q, %v", body, err)
	}
}

// Redirects are checked like the first URL.
func TestRedirects(t *testing.T) {
	var target string
	s, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			_, _ = io.WriteString(w, "final")
			return
		}
		http.Redirect(w, r, target, http.StatusFound)
	})
	port := s.URL[strings.LastIndex(s.URL, ":"):]
	g, _ := netguard.New([]string{"example.com"}, netguard.WithResolver(fakeDNS(t, netip.MustParseAddr("127.0.0.1"))))
	for _, to := range []string{
		"http://127.0.0.1" + port + "/final",          // not https
		"https://127.0.0.1" + port + "/final",         // an internal address
		"https://other.example.org" + port + "/final", // a name resolving to one
		"ftp://example.com/final",
	} {
		target = to
		if _, err := get(t, g, s, "https://example.com"+port+"/start"); !errors.Is(err, netguard.ErrBlocked) {
			t.Errorf("redirect to %s: %v, want ErrBlocked", to, err)
		}
	}
	target = "https://example.com" + port + "/final"
	if body, err := get(t, g, s, "https://example.com"+port+"/start"); err != nil || body != "final" {
		t.Errorf("redirect to an allowed host: %q, %v", body, err)
	}
	target = "/start"
	if _, err := get(t, g, s, "https://example.com"+port+"/start"); !errors.Is(err, netguard.ErrBlocked) {
		t.Errorf("redirect loop: %v", err)
	}
}

func TestNoProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	g, _ := netguard.New(nil)
	if tr := g.Client(nil).Transport.(*http.Transport); tr.Proxy != nil {
		t.Error("the client uses a proxy")
	}
}

func FuzzCheckURL(f *testing.F) {
	for _, s := range []string{"https://example.com/", "https://[::ffff:7f00:1]/", "https://0x7f.1/", "https://2130706433/", "https://127.1/"} {
		f.Add(s)
	}
	g, _ := netguard.New(nil)
	f.Fuzz(func(t *testing.T, raw string) {
		u, err := g.CheckURL(raw)
		if err != nil {
			return
		}
		if u.Scheme != "https" || u.User != nil || u.Hostname() == "" {
			t.Fatalf("accepted %q", raw)
		}
		if a, err := netip.ParseAddr(u.Hostname()); err == nil && netguard.Reason(a) != "" {
			t.Fatalf("accepted %q", raw)
		}
	})
}

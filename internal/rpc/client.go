package rpc

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"time"
)

// UserAgent identifies the crawler honestly to every node it dials and
// tells an operator where to find the methodology and the opt-out procedure.
const UserAgent = "nodemap/0 (+https://github.com/ny4rl4th0t3p/nodemap)"

// Client dials CometBFT RPC endpoints with a hard per-request timeout, no
// keep-alives, and no redirect following. Redirects are refused because a
// node could otherwise steer the crawler to an address it never advertised.
type Client struct {
	hc    *http.Client
	proxy func(*http.Request) (*url.URL, error)
}

// Conn describes the connection a request used, as far as the client can
// tell. RemoteIP is the address the TCP connection actually reached, which
// is the right address to enrich a seed at; it is empty when the request
// went through a proxy (the address would be the proxy's) or when the
// transport cannot be traced.
type Conn struct {
	RemoteIP string
	Proxied  bool
}

// NewClient returns a Client whose every request, including dial, TLS
// handshake, headers, and body, must complete within timeout. Proxy
// environment variables are honored and detected per request, so callers
// never mistake a proxy for a node.
func NewClient(timeout time.Duration) *Client {
	c := NewClientWithTransport(timeout, &http.Transport{
		DialContext:           (&net.Dialer{Timeout: timeout}).DialContext,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		DisableKeepAlives:     true,
		Proxy:                 http.ProxyFromEnvironment,
	})
	c.proxy = http.ProxyFromEnvironment
	return c
}

// NewClientWithTransport is NewClient with the transport supplied by the
// caller. It exists so tests can serve synthetic nodes in-process without
// listening sockets; production code uses NewClient. The timeout and the
// redirect refusal still apply. No proxy is assumed.
func NewClientWithTransport(timeout time.Duration, rt http.RoundTripper) *Client {
	return &Client{hc: &http.Client{
		Timeout:   timeout,
		Transport: rt,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// NetInfo fetches and decodes base + "/net_info".
func (c *Client) NetInfo(ctx context.Context, base string) (*NetInfo, error) {
	var v NetInfo
	if _, err := c.get(ctx, base, "/net_info", &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// Status fetches and decodes base + "/status" and reports the connection
// it used.
func (c *Client) Status(ctx context.Context, base string) (*Status, Conn, error) {
	var v Status
	conn, err := c.get(ctx, base, "/status", &v)
	if err != nil {
		return nil, Conn{}, err
	}
	return &v, conn, nil
}

func (c *Client) get(ctx context.Context, base, path string, v any) (Conn, error) {
	target := strings.TrimRight(base, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return Conn{}, fmt.Errorf("rpc: request: %w", err)
	}
	var conn Conn
	if c.proxy != nil {
		if u, perr := c.proxy(req); perr == nil && u != nil {
			conn.Proxied = true
		}
	}
	if !conn.Proxied {
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) {
				if addr, ok := info.Conn.RemoteAddr().(*net.TCPAddr); ok {
					conn.RemoteIP = addr.IP.String()
				}
			},
		}))
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return Conn{}, fmt.Errorf("rpc: do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Conn{}, fmt.Errorf("%w: %d", ErrHTTPStatus, resp.StatusCode)
	}
	return conn, decodeEnvelope(resp.Body, v)
}

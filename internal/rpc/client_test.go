package rpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestClientFetchesFixtures(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, UserAgent, r.Header.Get("User-Agent"))
		name := map[string]string{"/net_info": "net_info.json", "/status": "status.json"}[r.URL.Path]
		if name == "" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join("testdata", name))
	})
	c := NewClient(2 * time.Second)
	ctx := context.Background()
	ni, err := c.NetInfo(ctx, srv.URL+"/")
	require.NoError(t, err)
	assert.Len(t, ni.Peers, 10)
	st, conn, err := c.Status(ctx, srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "cosmoshub-4", st.NodeInfo.Network)
	assert.Equal(t, "127.0.0.1", conn.RemoteIP, "the traced address is the one the connection reached")
	assert.False(t, conn.Proxied)
}

func TestClientReportsNoAddressThroughAProxy(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join("testdata", "status.json"))
	})
	c := NewClient(2 * time.Second)
	// The transport dials directly; only the client's proxy decision says
	// "proxied", which is enough to prove the address is withheld.
	c.proxy = func(*http.Request) (*url.URL, error) { return url.Parse("http://proxy.test:3128") }
	_, conn, err := c.Status(context.Background(), srv.URL)
	require.NoError(t, err)
	assert.True(t, conn.Proxied)
	assert.Empty(t, conn.RemoteIP, "a proxy's address must never be taken for a node's")
}

func TestClientWithUntraceableTransportReportsNoAddress(t *testing.T) {
	rt := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		http.ServeFile(rec, httptest.NewRequestWithContext(req.Context(), http.MethodGet, "/status", http.NoBody),
			filepath.Join("testdata", "status.json"))
		return rec.Result(), nil
	})
	_, conn, err := NewClientWithTransport(time.Second, rt).Status(context.Background(), "http://192.0.2.1:26657")
	require.NoError(t, err)
	assert.Empty(t, conn.RemoteIP)
	assert.False(t, conn.Proxied)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientRejectsNon200(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	_, err := NewClient(time.Second).NetInfo(context.Background(), srv.URL)
	assert.ErrorIs(t, err, ErrHTTPStatus)
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	followed := false
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed = true
			http.ServeFile(w, r, filepath.Join("testdata", "net_info.json"))
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})
	_, err := NewClient(time.Second).NetInfo(context.Background(), srv.URL)
	assert.ErrorIs(t, err, ErrHTTPStatus)
	assert.False(t, followed, "redirect was followed")
}

func TestClientRejectsOversizedBody(t *testing.T) {
	big := strings.Repeat("{", MaxBody+1)
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(big))
	})
	_, err := NewClient(5*time.Second).NetInfo(context.Background(), srv.URL)
	assert.ErrorIs(t, err, ErrBodyTooLarge)
}

func TestClientTimesOut(t *testing.T) {
	srv := serve(t, func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	start := time.Now()
	_, err := NewClient(100*time.Millisecond).NetInfo(context.Background(), srv.URL)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 2*time.Second)
}

func TestClientHonorsContext(t *testing.T) {
	srv := serve(t, func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewClient(5*time.Second).NetInfo(ctx, srv.URL)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestClientRejectsUnreachable(t *testing.T) {
	// A closed port on loopback fails fast without touching the network.
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()
	_, err := NewClient(time.Second).NetInfo(context.Background(), addr)
	assert.Error(t, err)
}

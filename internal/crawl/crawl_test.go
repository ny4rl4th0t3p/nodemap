package crawl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/nodemap/internal/model"
	"github.com/ny4rl4th0t3p/nodemap/internal/rpc"
)

const chain = "test-1"

// fakeNet serves synthetic nodes in-process, keyed by host:port. Any other
// host is a refused connection. No sockets are opened.
type fakeNet map[string]http.Handler

func (f fakeNet) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	h, ok := f[req.URL.Host]
	if !ok {
		return nil, errors.New("dial tcp: connection refused")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result(), nil
}

// fakeResolver maps seed hostnames to IPs without DNS.
type fakeResolver map[string]string

func (f fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	ip, ok := f[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
}

type peerSpec struct {
	ip, listen, network, version, rpc string
}

// node builds a handler answering /status and /net_info for one synthetic
// node. The status carries a full validator_info block with the given
// voting power, and every peer carries an id, is_outbound, and
// connection_status, so that the reduction is shown dropping them, not
// merely never receiving them.
func node(id, votingPower, listen string, earliest int64, txIndex string, peers []peerSpec) http.Handler {
	status := fmt.Sprintf(`{"jsonrpc":"2.0","id":-1,"result":{"node_info":{"id":%q,"listen_addr":%q,`+
		`"network":%q,"version":"0.38.22","moniker":"m","other":{"tx_index":%q,"rpc_address":"tcp://0.0.0.0:26657"}},`+
		`"sync_info":{"latest_block_height":"100","earliest_block_height":"%d","catching_up":false},`+
		`"validator_info":{"address":"AA","pub_key":{"type":"x","value":"y"},"voting_power":%q}}}`,
		id, listen, chain, txIndex, earliest, votingPower)
	var ps []string
	for i, p := range peers {
		ps = append(ps, fmt.Sprintf(`{"node_info":{"id":"peer%d","listen_addr":%q,"network":%q,"version":%q,`+
			`"moniker":"m","other":{"tx_index":"on","rpc_address":%q}},"is_outbound":%v,`+
			`"connection_status":{"Duration":"1"},"remote_ip":%q}`,
			i, p.listen, p.network, p.version, p.rpc, i%2 == 0, p.ip))
	}
	netInfo := `{"jsonrpc":"2.0","id":-1,"result":{"peers":[` + strings.Join(ps, ",") + `]}}`
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/status":
			_, _ = w.Write([]byte(status))
		case "/net_info":
			_, _ = w.Write([]byte(netInfo))
		default:
			http.NotFound(w, r)
		}
	})
}

// fakeGeo enriches by IP and records which IPs were asked about.
type fakeGeo struct {
	mu    sync.Mutex
	table map[string]Enrichment
	asked map[string]int
}

func (g *fakeGeo) Lookup(ip string) Enrichment {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.asked == nil {
		g.asked = map[string]int{}
	}
	g.asked[ip]++
	return g.table[ip]
}

func (g *fakeGeo) askedAbout() map[string]int {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]int, len(g.asked))
	for k, v := range g.asked {
		out[k] = v
	}
	return out
}

const (
	std  = "tcp://0.0.0.0:26656"
	open = "tcp://0.0.0.0:26657"
)

func hetzner() Enrichment { return Enrichment{Country: "DE", ASN: 24940, ASOrg: "Hetzner"} }
func aws() Enrichment     { return Enrichment{Country: "US", ASN: 16509, ASOrg: "AWS"} }

// Topology. A is the seed. Public: A, B, C, G. D advertises an RPC that
// refuses connections. E binds its RPC to loopback so it is never dialed.
// F is on another chain and must be ignored entirely. G is public but has
// opted out.
//
//	A - B, A - C, A - D, A - E, B - C, C - G
func topology() (fakeNet, *fakeGeo) {
	a := peerSpec{"192.0.2.1", std, chain, "0.38.22", open}
	b := peerSpec{"192.0.2.2", std, chain, "0.38.22", open}
	c := peerSpec{"192.0.2.3", std, chain, "0.38.22", open}
	d := peerSpec{"192.0.2.4", std, chain, "0.37.0", open}
	e := peerSpec{"192.0.2.5", std, chain, "0.38.22", "tcp://127.0.0.1:26657"}
	f := peerSpec{"203.0.113.1", std, "other-1", "0.38.22", open}
	g := peerSpec{"192.0.2.7", std, chain, "0.38.22", open}
	world := fakeNet{
		"192.0.2.1:26657": node("idA", "0", std, 1, "on", []peerSpec{b, c, d, e, f}),
		"192.0.2.2:26657": node("idB", "0", std, 5000, "off", []peerSpec{a, c}),
		"192.0.2.3:26657": node("idC", "0", std, 1, "on", []peerSpec{a, b, g}),
		"192.0.2.7:26657": node("idG", "0", std, 1, "on", []peerSpec{c}),
	}
	geo := &fakeGeo{table: map[string]Enrichment{
		"192.0.2.1": hetzner(), "192.0.2.2": hetzner(), "192.0.2.3": hetzner(),
		"192.0.2.4": aws(), "192.0.2.5": aws(),
		"192.0.2.7": {Country: "FI"},
	}}
	return world, geo
}

func run(t *testing.T, world fakeNet, geo *fakeGeo, mod func(*Config)) *Result {
	t.Helper()
	cfg := Config{
		Chain:   chain,
		Seeds:   []string{"http://192.0.2.1:26657"},
		Workers: 4,
		TopN:    2,
		Client:  rpc.NewClientWithTransport(time.Second, world),
		Enrich:  geo,
		Suppressed: func(id string) bool {
			return id == "idG"
		},
	}
	if mod != nil {
		mod(&cfg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := Run(ctx, cfg)
	require.NoError(t, err)
	return r
}

func endpoints(r *Result) []string {
	out := make([]string, 0, len(r.Directory))
	for _, rec := range r.Directory {
		out = append(out, rec.Endpoint)
	}
	return out
}

func TestCrawlReducesTopology(t *testing.T) {
	world, geo := topology()
	r := run(t, world, geo, nil)

	assert.Equal(t, 1, r.SeedsAnswered)
	assert.Equal(t, 5, r.Dialed, "A, B, C, D, G; E is never dialed, F is ignored")
	assert.Equal(t, 4, r.PublicNodes)
	assert.Equal(t, 2, r.NonPublicNodes)

	require.Equal(t, []string{"http://192.0.2.1:26657", "http://192.0.2.2:26657", "http://192.0.2.3:26657"}, endpoints(r))
	assert.Equal(t, model.NodeRecord{
		Endpoint: "http://192.0.2.1:26657",
		Country:  "DE", ASN: 24940, ASOrg: "Hetzner", EarliestBlockHeight: 1, TxIndex: true,
	}, r.Directory[0])
	assert.Equal(t, int64(5000), r.Directory[1].EarliestBlockHeight)
	assert.False(t, r.Directory[1].TxIndex)

	assert.Equal(t, map[string]int{"DE": 3, "US": 2, "FI": 1}, r.Countries)
	assert.Equal(t, map[uint32]ASNCount{24940: {"Hetzner", 3}, 16509: {"AWS", 2}}, r.ASNs)
	assert.Equal(t, map[string]int{"0.38.22": 5, "0.37.0": 1}, r.Versions)

	assert.Equal(t, 6, r.Graph.Population)
	assert.InDelta(t, 1, r.Graph.LargestComponentFraction, delta)
	assert.Equal(t, 2, r.Graph.TopN)
	// Ten responder-to-peer pairs were reported (A:4, B:2, C:3, G:1), so 20
	// mentions. A collects 6 (its 4 plus B and C naming it), C collects 6
	// (its 3 plus A, B, and G naming it): top-2 = 12/20.
	assert.InDelta(t, 12.0/20.0, r.Graph.TopNShare, delta)

	// Every node is enriched exactly once, whether it was seen as a seed, as
	// a dialed peer, or from several vantage points; other chains never.
	assert.Equal(t, map[string]int{
		"192.0.2.1": 1, "192.0.2.2": 1, "192.0.2.3": 1, "192.0.2.4": 1, "192.0.2.5": 1, "192.0.2.7": 1,
	}, geo.askedAbout())
}

func TestCrawlWithNoAnsweringSeed(t *testing.T) {
	r := run(t, fakeNet{}, &fakeGeo{}, nil)
	assert.Equal(t, 0, r.SeedsAnswered)
	assert.Equal(t, 1, r.Dialed)
	assert.Equal(t, 0, r.PublicNodes)
	assert.Equal(t, 0, r.NonPublicNodes)
	assert.Empty(t, r.Directory)
	assert.Equal(t, 0, r.Graph.Population)
}

func TestCrawlIgnoresSeedOnAnotherChain(t *testing.T) {
	world, geo := topology()
	r := run(t, world, geo, func(c *Config) { c.Chain = "other-1" })
	assert.Equal(t, 0, r.SeedsAnswered)
	assert.Equal(t, 0, r.PublicNodes)
	assert.Empty(t, r.Directory)
}

// syntheticPeers returns n distinct peers on documentation IPs, none of
// which answer.
func syntheticPeers(n int) []peerSpec {
	peers := make([]peerSpec, 0, n)
	for i := range n {
		// Three /24 blocks that never collide with the 192.0.2.0/24 topology:
		// two documentation ranges and one from 100.64.0.0/10, which Go does
		// not classify as private.
		ip := fmt.Sprintf("%s.%d", []string{"203.0.113", "198.51.100", "100.64.0"}[i/254], i%254+1)
		peers = append(peers, peerSpec{ip, std, chain, "0.38.22", open})
	}
	return peers
}

func TestCrawlCapsNodes(t *testing.T) {
	world := fakeNet{"192.0.2.1:26657": node("idA", "0", std, 1, "on", syntheticPeers(50))}
	r := run(t, world, &fakeGeo{}, func(c *Config) { c.MaxNodes = 10 })
	assert.Equal(t, 10, r.PublicNodes+r.NonPublicNodes)
	assert.LessOrEqual(t, r.Dialed, 10)
	assert.Equal(t, 10, r.Graph.Population)
}

func TestCrawlCapsPeersPerResponse(t *testing.T) {
	world := fakeNet{"192.0.2.1:26657": node("idA", "0", std, 1, "on", syntheticPeers(maxPeersPerResponse+200))}
	r := run(t, world, &fakeGeo{}, nil)
	assert.Equal(t, 1, r.PublicNodes)
	assert.Equal(t, maxPeersPerResponse, r.NonPublicNodes, "the tail of an oversized peer list is ignored")
	assert.Equal(t, maxPeersPerResponse+1, r.Dialed)
	assert.Equal(t, maxPeersPerResponse+1, r.Graph.Population)
}

func TestCrawlResolvesSeedsInWorkersAndSurvivesDeadDNS(t *testing.T) {
	world, geo := topology()
	world["seed.test:26657"] = world["192.0.2.1:26657"]
	r := run(t, world, geo, func(c *Config) {
		c.Seeds = []string{"http://dead.test:26657", "http://seed.test:26657"}
		c.Resolver = fakeResolver{"seed.test": "192.0.2.1"} // dead.test does not resolve
	})
	assert.Equal(t, 1, r.SeedsAnswered)
	assert.Equal(t, 4, r.PublicNodes)
	assert.Equal(t, "http://seed.test:26657", r.Directory[2].Endpoint)
	assert.Equal(t, "DE", r.Directory[2].Country, "resolved in the worker and enriched")
}

func TestCrawlRecordsANodeOnceAndPrefersTheSeedURL(t *testing.T) {
	world, geo := topology()
	// The seed is a hostname for node A; B lists A by IP, so A is also dialed
	// as http://192.0.2.1:26657. Both URLs answer as the same node.
	world["seed.test:26657"] = world["192.0.2.1:26657"]
	r := run(t, world, geo, func(c *Config) {
		c.Seeds = []string{"http://seed.test:26657"}
		c.Resolver = fakeResolver{"seed.test": "192.0.2.1"}
	})
	assert.Equal(t, 6, r.Dialed, "seed.test, A by IP, B, C, D, G")
	assert.Equal(t, 4, r.PublicNodes)
	assert.Equal(t, 2, r.NonPublicNodes)
	require.Equal(t, []string{"http://192.0.2.2:26657", "http://192.0.2.3:26657", "http://seed.test:26657"}, endpoints(r))
	assert.Equal(t, "DE", r.Directory[2].Country)
}

func TestCrawlCountsButNeverListsASelfDeclaredValidator(t *testing.T) {
	world, geo := topology()
	// B answers with voting power: an open-RPC validator. It still counts as
	// a public node, its peers are still followed, but it has no record.
	world["192.0.2.2:26657"] = node("idB", "1485339", std, 5000, "off", []peerSpec{
		{"192.0.2.1", std, chain, "0.38.22", open}, {"192.0.2.3", std, chain, "0.38.22", open},
	})
	r := run(t, world, geo, nil)
	assert.Equal(t, 4, r.PublicNodes, "the validator answered RPC and is counted")
	assert.Equal(t, 5, r.Dialed)
	assert.Equal(t, []string{"http://192.0.2.1:26657", "http://192.0.2.3:26657"}, endpoints(r))
	assert.Equal(t, 3, r.Countries["DE"], "and it is enriched like any other node")
}

func TestCrawlPlacesANodeWhereItAdvertisesItself(t *testing.T) {
	// The seed resolves to a front door at 198.51.100.200 but the node
	// advertises its own public listen address 203.0.113.50. It is keyed,
	// enriched, and counted at the advertised address; the front door's
	// geography is never asked for.
	world, geo := topology()
	world["seed.test:26657"] = node("idA", "0", "203.0.113.50:26656", 1, "on", nil)
	geo.table["203.0.113.50"] = Enrichment{Country: "SG", ASN: 1, ASOrg: "Own"}
	geo.table["198.51.100.200"] = Enrichment{Country: "US", ASN: 2, ASOrg: "CDN"}
	r := run(t, world, geo, func(c *Config) {
		c.Seeds = []string{"http://seed.test:26657"}
		c.Resolver = fakeResolver{"seed.test": "198.51.100.200"}
	})
	require.Len(t, r.Directory, 1)
	assert.Equal(t, "SG", r.Directory[0].Country)
	assert.Equal(t, map[string]int{"SG": 1}, r.Countries)
	asked := geo.askedAbout()
	assert.Equal(t, 1, asked["203.0.113.50"])
	assert.Zero(t, asked["198.51.100.200"])
}

func TestRecordPrefersSeedOverPeerDiscovery(t *testing.T) {
	c := &crawler{directory: map[string]dirEntry{}}
	byIP := model.NodeRecord{Endpoint: "http://192.0.2.1:26657"}
	bySeed := model.NodeRecord{Endpoint: "http://seed.test:26657"}
	other := model.NodeRecord{Endpoint: "http://other.test:26657"}

	c.record("k", false, byIP)
	c.record("k", true, bySeed)
	assert.Equal(t, dirEntry{rec: bySeed, seed: true}, c.directory["k"], "seed replaces peer-discovered endpoint")
	c.record("k", true, other)
	c.record("k", false, byIP)
	assert.Equal(t, bySeed, c.directory["k"].rec, "first seed record is kept")
	c.record("j", false, byIP)
	c.record("j", false, other)
	assert.Equal(t, byIP, c.directory["j"].rec, "first peer-discovered record is kept")
}

func TestCrawlReportsProgressWithCountersOnly(t *testing.T) {
	world, geo := topology()
	var mu sync.Mutex
	var seen []Progress
	r := run(t, world, geo, func(c *Config) {
		c.ProgressEvery = time.Millisecond
		c.OnProgress = func(p Progress) {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, p)
		}
	})
	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, seen)
	last := seen[len(seen)-1]
	assert.Equal(t, 0, last.Pending)
	assert.Equal(t, r.Dialed, last.Dialed)
	assert.Equal(t, r.PublicNodes, last.Public)
	assert.Equal(t, r.NonPublicNodes, last.NonPublic)
	assert.Equal(t, len(r.Countries), last.Countries)
	assert.Positive(t, last.Elapsed)
	for i, p := range seen {
		assert.GreaterOrEqual(t, p.Pending, 0, "progress %d", i)
		assert.LessOrEqual(t, p.Dialed, r.Dialed, "progress %d", i)
		assert.LessOrEqual(t, p.Public, r.PublicNodes, "progress %d", i)
	}
}

func TestCrawlHonorsContext(t *testing.T) {
	world, geo := topology()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := Run(ctx, Config{
		Chain: chain, Seeds: []string{"http://192.0.2.1:26657"}, Workers: 2,
		Client: rpc.NewClientWithTransport(time.Second, world), Enrich: geo,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, r.SeedsAnswered, "a canceled run answers nothing")
}

func TestCrawlRejectsBadConfig(t *testing.T) {
	client := rpc.NewClient(time.Second)
	bad := map[string]Config{
		"empty":      {},
		"no client":  {Chain: chain, Seeds: []string{"x"}, Workers: 1},
		"no seeds":   {Chain: chain, Workers: 1, Client: client},
		"no workers": {Chain: chain, Seeds: []string{"x"}, Client: client},
		"no chain":   {Seeds: []string{"x"}, Workers: 1, Client: client},
	}
	for name, cfg := range bad {
		t.Run(name, func(t *testing.T) {
			_, err := Run(context.Background(), cfg)
			assert.ErrorIs(t, err, ErrConfig)
		})
	}
}

func TestCrawlSkipsInvalidSeedsButRunsTheRest(t *testing.T) {
	world, geo := topology()
	r := run(t, world, geo, func(c *Config) {
		c.Seeds = []string{"tcp://nope", "", "http://192.0.2.1:26657"}
	})
	assert.Equal(t, 1, r.SeedsAnswered)
	assert.Equal(t, 5, r.Dialed)
}

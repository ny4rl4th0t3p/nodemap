// Package crawl walks a chain's public RPC surface and reduces what it sees
// in-process, before anything is returned.
//
// The reduction is deliberately inline and unconditional in probe: a
// response is decoded, a few safe facts are taken from it, peers are
// pushed onto the work queue as addresses to probe on their own merits, and
// the response goes out of scope. No edge is kept, not even in memory: each
// reported connection is folded into per-node counters and forgotten. No
// node id is returned, no non-responder is ever more than a bump in a
// counter. There is no reducer to swap and no raw form to keep. This
// package is internal and exposes no crawl primitive.
package crawl

import (
	"context"
	"errors"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/ny4rl4th0t3p/nodemap/internal/model"
	"github.com/ny4rl4th0t3p/nodemap/internal/rpc"
)

const (
	defaultMaxNodes = 10000
	defaultTopN     = 5
	txIndexOn       = "on"
	// maxPeersPerResponse bounds what one responder can contribute. CometBFT
	// defaults allow a few dozen peers; a list ten times longer is either a
	// misconfiguration or an attempt to inflate the counters, and either way
	// the tail is ignored.
	maxPeersPerResponse = 500
)

// Enrichment is what IP enrichment yields. Empty or zero means unknown.
type Enrichment struct {
	Country string
	ASN     uint32
	ASOrg   string
}

// Enricher maps an IP to coarse, non-identifying metadata.
type Enricher interface {
	Lookup(ip string) Enrichment
}

// Resolver resolves seed hostnames. net.DefaultResolver satisfies it.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Config parameterizes one run.
type Config struct {
	// Chain is the chain-id; nodes and peers on any other network are ignored.
	Chain string
	// Seeds are operator-supplied RPC URLs to start from.
	Seeds []string
	// Workers bounds concurrent dials.
	Workers int
	// MaxNodes bounds how many distinct nodes are tracked and how many
	// addresses are dialed, so a hostile peer list cannot grow memory
	// without limit. Zero means defaultMaxNodes.
	MaxNodes int
	// TopN is the N for GraphStats.TopNShare. Zero means defaultTopN.
	TopN int
	// Client performs the dials.
	Client *rpc.Client
	// Enrich maps IPs to country and ASN. Nil means everything is unknown.
	Enrich Enricher
	// Suppressed reports whether a node id has opted out of an individual
	// record. The id is passed, compared, and forgotten. Nil means nobody.
	Suppressed func(nodeID string) bool
	// Resolver resolves seed hostnames. Nil means net.DefaultResolver.
	Resolver Resolver
	// OnProgress, when non-nil, is called every ProgressEvery with counters
	// only. It is the sole view into a running crawl and carries no address,
	// so it can be logged freely. Zero ProgressEvery disables it.
	OnProgress    func(Progress)
	ProgressEvery time.Duration
}

// Progress is a counters-only view of a running crawl.
type Progress struct {
	Elapsed   time.Duration
	Dialed    int
	Pending   int
	Public    int
	NonPublic int
	Countries int
}

// ASNCount is one ASN's node count and organization name.
type ASNCount struct {
	Org   string
	Nodes int
}

// Result is everything a run yields. It holds individual records for Tier A
// nodes only, and otherwise counters that carry no identity. Country, ASN,
// and version are separate counters on purpose: nothing here can join a
// version to a place.
type Result struct {
	// Directory lists Tier A nodes that have not opted out, sorted by endpoint.
	Directory []model.NodeRecord
	// SeedsAnswered is how many seeds answered on the configured chain.
	SeedsAnswered int
	// Dialed is how many distinct addresses were probed.
	Dialed int
	// PublicNodes answered their own RPC; NonPublicNodes were only seen as
	// someone's peer and are counted, never listed.
	PublicNodes    int
	NonPublicNodes int
	Countries      map[string]int
	ASNs           map[uint32]ASNCount
	Versions       map[string]int
	Graph          GraphStats
}

// ErrConfig reports an unusable Config.
var ErrConfig = errors.New("crawl: invalid config")

// target is one address to probe. host is an IP literal for peers and the
// seed's hostname for seeds; it is resolved by the worker, not up front, so
// slow or dead DNS never stalls the crawl.
type target struct {
	url  string
	host string
	seed bool
}

// nodeState is what the run remembers about one node under its transient
// key: whether it answered its own RPC, and its enrichment, looked up once.
type nodeState struct {
	public bool
	enr    Enrichment
}

// dirEntry is a node's directory record and whether its endpoint came from
// a seed. A node reached through several URLs gets one record; a seed URL
// (usually a hostname) replaces an IP literal discovered through a peer.
type dirEntry struct {
	rec  model.NodeRecord
	seed bool
}

type crawler struct {
	cfg  Config
	work chan target
	wg   sync.WaitGroup

	mu            sync.Mutex
	dialed        map[string]bool
	pending       int // enqueued, not yet probed
	nodes         map[string]nodeState
	public        int // nodes that answered their own RPC
	countries     map[string]int
	asns          map[uint32]ASNCount
	versions      map[string]int
	mesh          *mesh
	directory     map[string]dirEntry // by node key
	seedsAnswered int
}

// Run crawls until the work queue drains or ctx is done, then returns the
// reduced Result. A run in which no seed answers is not an error; the caller
// reads SeedsAnswered.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	if cfg.Chain == "" || cfg.Client == nil || len(cfg.Seeds) == 0 || cfg.Workers < 1 {
		return nil, ErrConfig
	}
	if cfg.MaxNodes <= 0 {
		cfg.MaxNodes = defaultMaxNodes
	}
	if cfg.TopN <= 0 {
		cfg.TopN = defaultTopN
	}
	if cfg.Resolver == nil {
		cfg.Resolver = net.DefaultResolver
	}
	c := &crawler{
		cfg:       cfg,
		work:      make(chan target, cfg.MaxNodes),
		dialed:    map[string]bool{},
		nodes:     map[string]nodeState{},
		countries: map[string]int{},
		asns:      map[uint32]ASNCount{},
		versions:  map[string]int{},
		mesh:      newMesh(),
		directory: map[string]dirEntry{},
	}
	start := time.Now()
	var workers sync.WaitGroup
	for range cfg.Workers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for t := range c.work {
				c.probe(ctx, t)
				c.mu.Lock()
				c.pending--
				c.mu.Unlock()
				c.wg.Done()
			}
		}()
	}
	stopProgress := c.startProgress(start)
	for _, raw := range cfg.Seeds {
		u, host, ok := seedTarget(raw)
		if !ok {
			continue
		}
		c.enqueue(target{url: u, host: host, seed: true})
	}
	c.wg.Wait()
	close(c.work)
	workers.Wait()
	stopProgress()
	return c.result(), nil
}

// startProgress runs the progress ticker when configured and returns the
// function that stops it. The final call happens after the crawl is done.
func (c *crawler) startProgress(start time.Time) func() {
	if c.cfg.OnProgress == nil || c.cfg.ProgressEvery <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(c.cfg.ProgressEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				c.cfg.OnProgress(c.progress(start))
			}
		}
	}()
	return func() {
		close(done)
		<-finished
		c.cfg.OnProgress(c.progress(start))
	}
}

func (c *crawler) progress(start time.Time) Progress {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Progress{
		Elapsed:   time.Since(start),
		Dialed:    len(c.dialed),
		Pending:   c.pending,
		Public:    c.public,
		NonPublic: len(c.nodes) - c.public,
		Countries: len(c.countries),
	}
}

// probe dials one address and reduces what comes back. This is the one
// place raw responses exist; everything below the two client calls is the
// reduction, and it is not configurable.
func (c *crawler) probe(ctx context.Context, t target) {
	if ctx.Err() != nil {
		return
	}
	st, conn, err := c.cfg.Client.Status(ctx, t.url)
	if err != nil || st.NodeInfo.Network != c.cfg.Chain {
		// Not a public service on this chain: nothing is stored about it.
		return
	}
	// The address the connection reached is the truth about where this
	// node is; resolution is only the fallback when the client could not
	// tell (proxied, or an untraceable transport).
	observed := conn.RemoteIP
	if observed == "" {
		observed = c.resolve(ctx, t.host)
	}
	key, ip := nodeKey(observed, st.NodeInfo.ListenAddr, t.url)
	e, tracked := c.observe(key, ip, st.NodeInfo.Version, true)
	if !tracked {
		return
	}
	// A node that reports voting power is a self-declared validator with an
	// open RPC. It counts in every aggregate like any other node, but it
	// never gets an individual record: the map must not be the place where
	// a misconfigured validator's address is published.
	listed := !st.Validator && (c.cfg.Suppressed == nil || !c.cfg.Suppressed(st.NodeInfo.ID))
	c.mu.Lock()
	if t.seed {
		c.seedsAnswered++
	}
	if listed {
		c.record(key, t.seed, model.NodeRecord{
			Endpoint:            t.url,
			Country:             e.Country,
			ASN:                 e.ASN,
			ASOrg:               e.ASOrg,
			EarliestBlockHeight: st.SyncInfo.EarliestBlockHeight,
			TxIndex:             st.NodeInfo.Other.TxIndex == txIndexOn,
			CatchingUp:          st.SyncInfo.CatchingUp,
		})
	}
	c.mu.Unlock()

	ni, err := c.cfg.Client.NetInfo(ctx, t.url)
	if err != nil {
		return
	}
	peers := ni.Peers
	if len(peers) > maxPeersPerResponse {
		peers = peers[:maxPeersPerResponse]
	}
	for i := range peers {
		p := &peers[i]
		if p.NodeInfo.Network != c.cfg.Chain {
			continue
		}
		pkey, pip := nodeKey(p.RemoteIP, p.NodeInfo.ListenAddr, "")
		if pkey == "" {
			continue
		}
		if _, tracked := c.observe(pkey, pip, p.NodeInfo.Version, false); !tracked {
			continue
		}
		c.mu.Lock()
		c.mesh.connect(key, pkey)
		c.mu.Unlock()
		if u, ok := dialTarget(p.RemoteIP, p.NodeInfo.Other.RPCAddress); ok {
			c.enqueue(target{url: u, host: p.RemoteIP})
		}
	}
	// st and ni go out of scope here. Peer ids were never decoded; the
	// remote IPs were consumed as dial targets and counters, and each
	// responder-to-peer pair was folded into the mesh and forgotten.
}

// record stores one directory record per node. The first sighting wins,
// except that a seed endpoint replaces one discovered through a peer, so the
// published endpoint is the operator-facing URL whenever one is known.
// Caller holds c.mu.
func (c *crawler) record(key string, seed bool, rec model.NodeRecord) {
	if cur, ok := c.directory[key]; ok && (cur.seed || !seed) {
		return
	}
	c.directory[key] = dirEntry{rec: rec, seed: seed}
}

// observe counts a node once, enriching its IP on first sight, and records
// whether it answered its own RPC. It returns the node's enrichment and
// false when the node is new but the cap is reached.
func (c *crawler) observe(key, ip, version string, public bool) (Enrichment, bool) {
	c.mu.Lock()
	if s, known := c.nodes[key]; known {
		if public && !s.public {
			s.public = true
			c.nodes[key] = s
			c.public++
		}
		c.mu.Unlock()
		return s.enr, true
	}
	if len(c.nodes) >= c.cfg.MaxNodes {
		c.mu.Unlock()
		return Enrichment{}, false
	}
	// Reserve the key before the lookup so a concurrent observer of the
	// same node sees it as known and does not count it again.
	c.nodes[key] = nodeState{public: public}
	if public {
		c.public++
	}
	c.mu.Unlock()

	e := c.enrich(ip)
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.nodes[key]
	s.enr = e
	c.nodes[key] = s
	if e.Country != "" {
		c.countries[e.Country]++
	}
	if e.ASN != 0 {
		a := c.asns[e.ASN]
		a.Nodes++
		if a.Org == "" {
			a.Org = e.ASOrg
		}
		c.asns[e.ASN] = a
	}
	if version != "" {
		c.versions[version]++
	}
	c.mesh.touch(key)
	return e, true
}

// enqueue schedules one dial per address per run, within the node cap.
func (c *crawler) enqueue(t target) {
	c.mu.Lock()
	if c.dialed[t.url] || len(c.dialed) >= c.cfg.MaxNodes {
		c.mu.Unlock()
		return
	}
	c.dialed[t.url] = true
	c.pending++
	c.mu.Unlock()
	c.wg.Add(1)
	c.work <- t
}

func (c *crawler) enrich(ip string) Enrichment {
	if c.cfg.Enrich == nil || ip == "" {
		return Enrichment{}
	}
	return c.cfg.Enrich.Lookup(ip)
}

// resolve returns host itself when it is an IP literal, otherwise its first
// resolved address, or "" when resolution fails. It runs in the worker, so
// a slow resolver costs one worker's time, not the whole crawl's start.
func (c *crawler) resolve(ctx context.Context, host string) string {
	if host == "" || net.ParseIP(host) != nil {
		return host
	}
	addrs, err := c.cfg.Resolver.LookupIPAddr(ctx, host)
	if err != nil || len(addrs) == 0 {
		return ""
	}
	return addrs[0].IP.String()
}

func (c *crawler) result() *Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	dir := make([]model.NodeRecord, 0, len(c.directory))
	for _, d := range c.directory {
		dir = append(dir, d.rec)
	}
	r := &Result{
		Directory:      dir,
		SeedsAnswered:  c.seedsAnswered,
		Dialed:         len(c.dialed),
		PublicNodes:    c.public,
		NonPublicNodes: len(c.nodes) - c.public,
		Countries:      c.countries,
		ASNs:           c.asns,
		Versions:       c.versions,
		Graph:          c.mesh.stats(c.cfg.TopN),
	}
	sort.Slice(r.Directory, func(i, j int) bool { return r.Directory[i].Endpoint < r.Directory[j].Endpoint })
	return r
}

// Command nodemap crawls the public RPC surface of one CometBFT chain and
// writes a reduced, aggregate-only dataset.
//
// Everything sensitive is dropped in-process before anything is written; the
// persisted types in internal/model have no place for peer edges, node ids,
// validator identities, or per-node software versions. That absence is the
// safeguard, so nothing in this program is allowed to widen those types.
//
// Exit codes: 0 crawl completed; 1 the crawl did not complete, because no
// seed answered or because it was stopped by a signal or the deadline before
// the queue drained (nothing is written, so a bad run never replaces a good
// snapshot or pollutes the history); 2 error (bad input, unusable
// configuration, I/O failure).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ny4rl4th0t3p/nodemap/internal/agg"
	"github.com/ny4rl4th0t3p/nodemap/internal/crawl"
	"github.com/ny4rl4th0t3p/nodemap/internal/geo"
	"github.com/ny4rl4th0t3p/nodemap/internal/output"
	"github.com/ny4rl4th0t3p/nodemap/internal/rpc"
	"github.com/ny4rl4th0t3p/nodemap/internal/suppress"
)

const (
	exitOK            = 0
	exitFail          = 1
	exitError         = 2
	saltEnvVar        = "NODEMAP_SALT"
	defaultWorkers    = 16
	defaultTimeout    = 5 * time.Second
	defaultMaxRuntime = 20 * time.Minute
	defaultProgress   = 30 * time.Second
	defaultDownWindow = 24 * time.Hour
	maxInputBytes     = 1 << 20
)

// The FAIL tier: the crawl ran but produced nothing publishable.
var (
	errNoSeeds    = errors.New("no seed answered")
	errIncomplete = errors.New("crawl stopped before the queue drained")
)

type config struct {
	seeds      string
	chain      string
	geoCountry string
	geoASN     string
	suppress   string
	out        string
	workers    int
	timeout    time.Duration
	maxRuntime time.Duration
	progress   time.Duration
	downWindow time.Duration
	salt       string
	hash       bool
}

func main() {
	cfg := parseFlags(os.Args[1:])
	var err error
	if cfg.hash {
		err = hashMode(os.Stdin, os.Stdout, cfg.salt)
	} else {
		err = run(context.Background(), cfg, rpc.NewClient(cfg.timeout), os.Stderr)
	}
	switch {
	case err == nil:
		os.Exit(exitOK)
	case errors.Is(err, errNoSeeds), errors.Is(err, errIncomplete):
		fmt.Fprintln(os.Stderr, "nodemap: FAIL:", err)
		os.Exit(exitFail)
	default:
		fmt.Fprintln(os.Stderr, "nodemap: ERROR:", err)
		os.Exit(exitError)
	}
}

func parseFlags(args []string) config {
	var cfg config
	fs := flag.NewFlagSet("nodemap", flag.ExitOnError)
	fs.StringVar(&cfg.seeds, "seeds", "", "seed file in chain.json shape: chain_id, apis.rpc[].address (required)")
	fs.StringVar(&cfg.chain, "chain", "", "chain-id to crawl; peers on other networks are dropped (required)")
	fs.StringVar(&cfg.geoCountry, "geo-country", "", "country .mmdb (DB-IP Lite or GeoLite2); empty = unknown")
	fs.StringVar(&cfg.geoASN, "geo-asn", "", "ASN .mmdb (DB-IP Lite or GeoLite2); empty = unknown")
	fs.StringVar(&cfg.suppress, "suppress", "", "opt-out list: one salted node-id hash per line; empty = nobody")
	fs.StringVar(&cfg.out, "out", "out", "output directory for current.json, history.jsonl, directory-state.json")
	fs.IntVar(&cfg.workers, "workers", defaultWorkers, "concurrent dials")
	fs.DurationVar(&cfg.timeout, "timeout", defaultTimeout, "per-dial timeout")
	fs.DurationVar(&cfg.maxRuntime, "max-runtime", defaultMaxRuntime, "abandon the crawl after this long")
	fs.DurationVar(&cfg.progress, "progress", defaultProgress, "print counters to stderr this often; 0 = silent")
	fs.DurationVar(&cfg.downWindow, "down-window", defaultDownWindow,
		"how long a vanished public endpoint stays listed as down before it is forgotten")
	fs.BoolVar(&cfg.hash, "hash", false, "read a node id from stdin, print the opt-out list entry for it, exit")
	_ = fs.Parse(args)
	cfg.salt = os.Getenv(saltEnvVar)
	return cfg
}

// hashMode produces one opt-out list entry. Deciding that a node may be
// delisted is the instance's process, whatever it is; this only turns the
// resulting node id into the entry the crawler understands. The id arrives
// on stdin, never on a command line, and only the hash leaves.
func hashMode(in io.Reader, out io.Writer, salt string) error {
	if salt == "" {
		return errors.New(saltEnvVar + " is not set")
	}
	id, err := bufio.NewReader(io.LimitReader(in, maxInputBytes)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if strings.TrimSpace(id) == "" {
		return errors.New("no node id on stdin")
	}
	_, err = fmt.Fprintln(out, suppress.Hash(salt, id))
	return err
}

func run(ctx context.Context, cfg config, client *rpc.Client, stderr io.Writer) error {
	if cfg.seeds == "" || cfg.chain == "" {
		return errors.New("-seeds and -chain are required")
	}
	if cfg.workers < 1 || cfg.timeout <= 0 || cfg.maxRuntime <= 0 || cfg.downWindow < 0 {
		return errors.New("workers must be >= 1, timeouts > 0, down-window >= 0")
	}
	seeds, err := loadSeeds(cfg.seeds, cfg.chain)
	if err != nil {
		return err
	}
	enrich, closeGeo, err := openGeo(cfg)
	if err != nil {
		return err
	}
	defer closeGeo()
	sup, err := suppress.Load(cfg.suppress, cfg.salt)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, cfg.maxRuntime)
	defer cancel()

	start := time.Now()
	res, err := crawl.Run(ctx, crawl.Config{
		Chain:         cfg.chain,
		Seeds:         seeds,
		Workers:       cfg.workers,
		Client:        client,
		Enrich:        enrich,
		Suppressed:    sup.Suppressed,
		ProgressEvery: cfg.progress,
		OnProgress: func(p crawl.Progress) {
			_, _ = fmt.Fprintf(stderr, "nodemap: t=%s dialed=%d pending=%d public=%d non_public=%d countries=%d\n",
				p.Elapsed.Round(time.Second), p.Dialed, p.Pending, p.Public, p.NonPublic, p.Countries)
		},
	})
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return fmt.Errorf("%w: %w after %s (%d dialed, %d public)",
			errIncomplete, ctx.Err(), time.Since(start).Round(time.Second), res.Dialed, res.PublicNodes)
	}
	if res.SeedsAnswered == 0 {
		return fmt.Errorf("%w (%d seeds, %d dialed)", errNoSeeds, len(seeds), res.Dialed)
	}

	now := time.Now().UTC()
	cur, line := agg.Build(res, cfg.chain, now, agg.DefaultParams)
	if err := output.WriteCurrent(cfg.out, &cur); err != nil {
		return err
	}
	if err := output.AppendHistory(cfg.out, &line); err != nil {
		return err
	}
	live := make([]string, 0, len(cur.Directory))
	for _, rec := range cur.Directory {
		live = append(live, rec.Endpoint)
	}
	if _, err := output.UpdateDirectoryState(cfg.out, now, live, cfg.downWindow); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "nodemap: chain=%s seeds=%d/%d dialed=%d public=%d non_public=%d countries=%d elapsed=%s\n",
		cfg.chain, res.SeedsAnswered, len(seeds), res.Dialed, res.PublicNodes, res.NonPublicNodes,
		len(res.Countries), time.Since(start).Round(time.Second))
	return nil
}

func openGeo(cfg config) (crawl.Enricher, func(), error) {
	if cfg.geoCountry == "" && cfg.geoASN == "" {
		return geo.None{}, func() {}, nil
	}
	m, err := geo.Open(cfg.geoCountry, cfg.geoASN)
	if err != nil {
		return nil, nil, err
	}
	return m, func() { _ = m.Close() }, nil
}

// seedFile is the subset of a chain-registry chain.json the crawler reads,
// so a registry entry can be used as a seed file unchanged.
type seedFile struct {
	ChainID string `json:"chain_id"`
	APIs    struct {
		RPC []struct {
			Address string `json:"address"`
		} `json:"rpc"`
	} `json:"apis"`
}

// loadSeeds reads the seed addresses and refuses a file whose chain_id
// disagrees with the chain being crawled, so a seeds file cannot be pointed
// at the wrong network by a typo in a workflow.
func loadSeeds(path, chain string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("seeds: %w", err)
	}
	defer f.Close()
	var sf seedFile
	if err := json.NewDecoder(io.LimitReader(f, maxInputBytes)).Decode(&sf); err != nil {
		return nil, fmt.Errorf("seeds: %s: %w", path, err)
	}
	if sf.ChainID != "" && sf.ChainID != chain {
		return nil, fmt.Errorf("seeds: %s is for chain %q, not %q", path, sf.ChainID, chain)
	}
	seeds := make([]string, 0, len(sf.APIs.RPC))
	for _, r := range sf.APIs.RPC {
		if r.Address != "" {
			seeds = append(seeds, r.Address)
		}
	}
	if len(seeds) == 0 {
		return nil, fmt.Errorf("seeds: %s: no apis.rpc[].address entries", path)
	}
	return seeds, nil
}

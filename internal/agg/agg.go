// Package agg turns a crawl result into the persisted shapes, applying the
// publication rules of the security boundary: country counts are ungated,
// ASN rows and version buckets are k-anonymity gated, version adoption and
// the graph scalars are withheld below a population floor, endpoints are
// normalized so no credential is published, and nothing is ever
// cross-tabulated because the inputs arrive as separate counters.
package agg

import (
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/ny4rl4th0t3p/nodemap/internal/crawl"
	"github.com/ny4rl4th0t3p/nodemap/internal/model"
)

// Params are the publication thresholds.
type Params struct {
	// K is the minimum node count for an ASN row to be published.
	K int
	// MaxASNRows caps the ASN table at the largest ASNs. It is a page-size
	// limit, unrelated to the graph's top-N peer share.
	MaxASNRows int
	// PopulationFloor is the minimum population below which version
	// adoption and graph scalars are withheld entirely: on a thin chain
	// they would fingerprint individual nodes.
	PopulationFloor int
}

// DefaultParams are the boundary document's suggested values.
var DefaultParams = Params{K: 5, MaxASNRows: 10, PopulationFloor: 20}

// OtherVersion is the bucket for every version string that is not a
// CometBFT release the network could plausibly run.
const OtherVersion = "other"

// releaseRE matches a semantic version with an optional leading v, an
// optional pre-release (chains name mainnet releases like
// v1.20.3-safeharbor.2), and optional build metadata, which is dropped. The
// pre-release is bounded so a hostile string never becomes a bucket name;
// the per-bucket k rule already keeps a suffix unique to one operator from
// being published.
var releaseRE = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.-]{1,32})?(\+[0-9A-Za-z.-]+)?$`)

// oldestMinor is the oldest 0.x line still seen on live networks
// (Tendermint 0.34). Anything older, or any placeholder like 0.0.1, is not
// a release string worth its own bucket.
const oldestMinor = 34

// Build reduces r into the snapshot and the history line for one run.
func Build(r *crawl.Result, chain string, at time.Time, p Params) (model.Current, model.HistoryLine) {
	cur := model.Current{
		Aggregates: model.Aggregates{
			SchemaVersion:  model.SchemaVersion,
			Chain:          chain,
			CrawledAt:      at,
			PublicNodes:    r.PublicNodes,
			NonPublicNodes: r.NonPublicNodes,
			Countries:      countries(r),
			ASNs:           asns(r, p),
			Versions:       adoption(r.Versions, bucketVersion, p),
			AppVersions:    adoption(r.AppVersions, bucketRelease, p),
			Graph:          graph(r, p),
		},
		Directory: directory(r),
	}
	line := model.HistoryLine{
		SchemaVersion:  model.SchemaVersion,
		At:             at,
		Chain:          chain,
		PublicNodes:    r.PublicNodes,
		NonPublicNodes: r.NonPublicNodes,
	}
	if cur.Versions != nil {
		line.VersionShares = cur.Versions.Shares
	}
	if cur.AppVersions != nil {
		line.AppVersionShares = cur.AppVersions.Shares
	}
	if cur.Graph != nil {
		lcc, top := cur.Graph.LargestComponentFraction, cur.Graph.TopNShare
		line.LargestComponentFraction = &lcc
		line.TopNShare = &top
	}
	return cur, line
}

func countries(r *crawl.Result) map[string]int {
	out := make(map[string]int, len(r.Countries))
	for c, n := range r.Countries {
		out[c] = n
	}
	return out
}

// asns publishes at most MaxASNRows rows, each with at least K nodes,
// ordered by node count descending then ASN ascending so the output is
// deterministic.
// Share is over every node observed, so rows need not sum to one: nodes
// with no known ASN are simply absent. ConnectionShare is over all reported
// connections and is published only when the graph is, since it derives
// from the same mesh and carries the same thin-chain risk.
func asns(r *crawl.Result, p Params) []model.ASNShare {
	total := r.PublicNodes + r.NonPublicNodes
	withGraph := graph(r, p) != nil && r.Graph.Mentions > 0
	rows := make([]model.ASNShare, 0, len(r.ASNs))
	for asn, c := range r.ASNs {
		if c.Nodes < p.K || total == 0 {
			continue
		}
		row := model.ASNShare{
			ASN:   asn,
			Org:   c.Org,
			Nodes: c.Nodes,
			Share: float64(c.Nodes) / float64(total),
		}
		if withGraph {
			row.ConnectionShare = float64(c.Mentions) / float64(r.Graph.Mentions)
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Nodes != rows[j].Nodes {
			return rows[i].Nodes > rows[j].Nodes
		}
		return rows[i].ASN < rows[j].ASN
	})
	if len(rows) > p.MaxASNRows {
		rows = rows[:p.MaxASNRows]
	}
	return rows
}

// bucketRelease maps a version string to its canonical
// "major.minor.patch[-prerelease]" when it is a release string, and to
// OtherVersion otherwise. It is the rule for application versions, which
// share no release history.
func bucketRelease(v string) string {
	m := releaseRE.FindStringSubmatch(v)
	if m == nil {
		return OtherVersion
	}
	return m[1] + "." + m[2] + "." + m[3] + m[4]
}

// bucketVersion is bucketRelease with the CometBFT minimum line: a 0.x
// version older than oldestMinor is not a release the network runs.
func bucketVersion(v string) string {
	m := releaseRE.FindStringSubmatch(v)
	if m == nil {
		return OtherVersion
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major == 0 && minor < oldestMinor {
		return OtherVersion
	}
	return m[1] + "." + m[2] + "." + m[3] + m[4]
}

// adoption turns raw version counts into published shares: nil below the
// population floor, and every named bucket with fewer than K nodes folded
// into OtherVersion, so a rare build is never named. OtherVersion itself is
// published at any size, since it names nothing.
func adoption(counts map[string]int, bucket func(string) string, p Params) *model.VersionAdoption {
	population := 0
	buckets := map[string]int{}
	for v, n := range counts {
		population += n
		buckets[bucket(v)] += n
	}
	if population < p.PopulationFloor || population == 0 {
		return nil
	}
	folded := map[string]int{}
	for b, n := range buckets {
		if b != OtherVersion && n < p.K {
			folded[OtherVersion] += n
		} else {
			folded[b] += n
		}
	}
	shares := make(map[string]float64, len(folded))
	for b, n := range folded {
		shares[b] = float64(n) / float64(population)
	}
	return &model.VersionAdoption{Population: population, Shares: shares}
}

// graph publishes the two connectivity scalars only above the floor.
func graph(r *crawl.Result, p Params) *model.GraphHealth {
	if r.Graph.Population < p.PopulationFloor || r.Graph.Population == 0 {
		return nil
	}
	return &model.GraphHealth{
		Population:               r.Graph.Population,
		LargestComponentFraction: r.Graph.LargestComponentFraction,
		TopN:                     r.Graph.TopN,
		TopNShare:                r.Graph.TopNShare,
	}
}

// directory copies the Tier A records with normalized endpoints, dropping
// any whose endpoint cannot be published, and keeps one record per
// normalized endpoint.
func directory(r *crawl.Result) []model.NodeRecord {
	out := make([]model.NodeRecord, 0, len(r.Directory))
	seen := map[string]bool{}
	for _, rec := range r.Directory {
		ep, ok := normalizeEndpoint(rec.Endpoint)
		if !ok || seen[ep] {
			continue
		}
		seen[ep] = true
		rec.Endpoint = ep
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Endpoint < out[j].Endpoint })
	return out
}

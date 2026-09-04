// Package model holds the only types that are ever persisted.
//
// Absence enforces the security boundary here: there is no field for a
// peer edge, a node id, a validator identity, a non-responder's IP, or a
// per-node software version. model_test.go walks every persisted type and
// fails if such a field is ever added. Widening these types is a design
// regression, not a feature.
package model

import (
	"reflect"
	"time"
)

// SchemaVersion stamps every persisted file. A consumer that pins a crawler
// release knows which shape it reads, and refuses one it does not know.
// Bump it when a field changes meaning or a file changes shape.
const SchemaVersion = 1

// Document is the set of types that may reach disk. The marker method is
// unexported, so only types in this package can be written at all; the
// output package writes nothing else, and refuses any Document that is not
// in Persisted. Persisted is the trust root of every guarantee below: the
// tests walk exactly these types, and a type cannot be written without being
// one of them.
type Document interface{ persisted() }

// Persisted is the registry of every type that reaches disk. The marker
// methods are at the end of this file, after the types.
var Persisted = []Document{Current{}, HistoryLine{}, DirectoryState{}}

// Registered reports whether d's type, or the type it points to, is in
// Persisted. The output package checks it before encoding anything.
func Registered(d Document) bool {
	t := reflect.TypeOf(d)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	for _, p := range Persisted {
		if reflect.TypeOf(p) == t {
			return true
		}
	}
	return false
}

// NodeRecord is the individual record of one Tier A node: a node that
// answered its own public RPC and thereby published itself as a service.
// Nothing about other nodes is ever attached to it.
type NodeRecord struct {
	// Endpoint is the RPC URL that answered, normalized for publication.
	// There is deliberately no moniker: it is the one field that would name
	// an operator next to an address, and the map does not need it.
	Endpoint string `json:"endpoint"`
	// Country is ISO 3166-1 alpha-2 from IP enrichment; empty when unknown.
	Country string `json:"country,omitempty"`
	// ASN is the autonomous system number from IP enrichment; 0 when unknown.
	ASN uint32 `json:"asn,omitempty"`
	// ASOrg is the autonomous system name from IP enrichment.
	ASOrg string `json:"as_org,omitempty"`
	// EarliestBlockHeight distinguishes archive (1) from pruned nodes.
	EarliestBlockHeight int64 `json:"earliest_block_height"`
	// TxIndex reports whether the node runs a transaction indexer.
	TxIndex bool `json:"tx_index"`
	// CatchingUp is the node's own sync flag at crawl time.
	CatchingUp bool `json:"catching_up"`
}

// ASNShare is one row of the ASN concentration table. Rows below the
// k-anonymity gate are never emitted.
type ASNShare struct {
	ASN   uint32  `json:"asn"`
	Org   string  `json:"org,omitempty"`
	Nodes int     `json:"nodes"`
	Share float64 `json:"share"`
	// ConnectionShare is the share of all reported peer connections that
	// land on nodes hosted in this ASN: where the mesh sits, not only where
	// the nodes are. Omitted below the population floor, with the graph.
	ConnectionShare float64 `json:"connection_share,omitempty"`
}

// VersionAdoption is network-wide client-version adoption. It is a
// standalone struct with no geography, ASN, or endpoint key, precisely so
// that it can never be joined to one. Nil in Current when the population is
// below the floor.
type VersionAdoption struct {
	Population int                `json:"population"`
	Shares     map[string]float64 `json:"shares"`
}

// GraphHealth carries the two connectivity scalars. They are folded from
// reported connections one at a time during the crawl; no edge set exists
// in memory or in any persisted type.
type GraphHealth struct {
	Population               int     `json:"population"`
	LargestComponentFraction float64 `json:"largest_component_fraction"`
	TopN                     int     `json:"top_n"`
	TopNShare                float64 `json:"top_n_share"`
}

// Aggregates is the bucketed view drawn from every node the crawl saw,
// including the non-public ones that have no individual record.
type Aggregates struct {
	SchemaVersion int       `json:"schema_version"`
	Chain         string    `json:"chain"`
	CrawledAt     time.Time `json:"crawled_at"`
	// PublicNodes is the Tier A count; NonPublicNodes is everything else
	// observed. Tiers B and C are deliberately not split: both are
	// aggregate-only, and telling them apart would need a P2P dial per peer.
	PublicNodes    int `json:"public_nodes"`
	NonPublicNodes int `json:"non_public_nodes"`
	// Countries is ungated: a country count is not an individual identifier.
	Countries map[string]int `json:"countries"`
	// ASNs is the top-N share table, k-gated.
	ASNs []ASNShare `json:"asns"`
	// Versions is the client (CometBFT) version adoption; nil below the
	// population floor.
	Versions *VersionAdoption `json:"versions,omitempty"`
	// AppVersions is the application version adoption, known for nodes that
	// answer RPC only; nil below its own population floor.
	AppVersions *VersionAdoption `json:"app_versions,omitempty"`
	// Graph is nil below the population floor.
	Graph *GraphHealth `json:"graph,omitempty"`
}

// Current is the whole snapshot written to current.json each run. It is
// replaced, never appended, so it carries no history.
type Current struct {
	Aggregates
	// Directory lists Tier A nodes only.
	Directory []NodeRecord `json:"directory"`
}

// DirectoryState remembers when each self-advertised endpoint last answered,
// so the map can show an endpoint that vanished as "down" for a short
// window before it disappears. It holds endpoint strings and times only,
// for endpoints that published themselves and are pruned every run.
type DirectoryState struct {
	SchemaVersion int                  `json:"schema_version"`
	Endpoints     map[string]time.Time `json:"endpoints"`
}

// HistoryLine is one appended line of history.jsonl: a timestamp and
// aggregate counter only. It has no per-node content of any kind.
type HistoryLine struct {
	SchemaVersion  int       `json:"schema_version"`
	At             time.Time `json:"at"`
	Chain          string    `json:"chain"`
	PublicNodes    int       `json:"public_nodes"`
	NonPublicNodes int       `json:"non_public_nodes"`
	// VersionShares and AppVersionShares are nil below their population floors.
	VersionShares    map[string]float64 `json:"version_shares,omitempty"`
	AppVersionShares map[string]float64 `json:"app_version_shares,omitempty"`
	// Graph scalars are nil below the population floor.
	LargestComponentFraction *float64 `json:"largest_component_fraction,omitempty"`
	TopNShare                *float64 `json:"top_n_share,omitempty"`
}

// The Document markers: exactly the types in Persisted, and nothing else.
func (Current) persisted()        {}
func (HistoryLine) persisted()    {}
func (DirectoryState) persisted() {}

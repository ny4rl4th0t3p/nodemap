package agg

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/nodemap/internal/crawl"
	"github.com/ny4rl4th0t3p/nodemap/internal/model"
)

var at = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

const delta = 1e-9

// sample is a result with 30 observed nodes: 12 public, 18 not.
func sample() *crawl.Result {
	return &crawl.Result{
		Directory: []model.NodeRecord{
			{Endpoint: "http://192.0.2.1:26657", Country: "DE", ASN: 24940},
			{Endpoint: "https://RPC.Example.test:443/", Country: "US", ASN: 16509},
		},
		SeedsAnswered:  1,
		Dialed:         14,
		PublicNodes:    12,
		NonPublicNodes: 18,
		Countries:      map[string]int{"DE": 14, "US": 9, "FI": 4, "SG": 1},
		ASNs: map[uint32]crawl.ASNCount{
			24940: {Org: "Hetzner", Nodes: 12, Mentions: 40},
			16509: {Org: "AWS", Nodes: 6, Mentions: 20},
			14061: {Org: "DigitalOcean", Nodes: 5, Mentions: 10},
			20473: {Org: "Vultr", Nodes: 4, Mentions: 8}, // below k
			64512: {Org: "", Nodes: 1, Mentions: 2},      // below k
		},
		Versions: map[string]int{"0.38.22": 20, "0.38.17": 6, "0.37.6": 4}, // 0.37.6 is below k
		// Responders only: 28 of the 30 reported an app version.
		AppVersions: map[string]int{"v25.1.0": 20, "25.1.0": 4, "25.0.0": 3, "garbage": 1},
		Graph: crawl.GraphStats{
			Population: 30, LargestComponentFraction: 0.9, TopN: 5, TopNShare: 0.55, Mentions: 100,
		},
	}
}

func TestBuildAppliesThresholds(t *testing.T) {
	cur, line := Build(sample(), "test-1", at, DefaultParams)

	assert.Equal(t, model.SchemaVersion, cur.SchemaVersion)
	assert.Equal(t, model.SchemaVersion, line.SchemaVersion)
	assert.Equal(t, "test-1", cur.Chain)
	assert.True(t, cur.CrawledAt.Equal(at))
	assert.Equal(t, 12, cur.PublicNodes)
	assert.Equal(t, 18, cur.NonPublicNodes)
	assert.Equal(t, map[string]int{"DE": 14, "US": 9, "FI": 4, "SG": 1}, cur.Countries, "countries are ungated")

	require.Len(t, cur.ASNs, 3, "rows below k are withheld")
	wantASNs := []model.ASNShare{
		{ASN: 24940, Org: "Hetzner", Nodes: 12, Share: 12.0 / 30, ConnectionShare: 0.4},
		{ASN: 16509, Org: "AWS", Nodes: 6, Share: 6.0 / 30, ConnectionShare: 0.2},
		{ASN: 14061, Org: "DigitalOcean", Nodes: 5, Share: 5.0 / 30, ConnectionShare: 0.1},
	}
	for i, w := range wantASNs {
		assert.Equal(t, w.ASN, cur.ASNs[i].ASN)
		assert.Equal(t, w.Org, cur.ASNs[i].Org)
		assert.Equal(t, w.Nodes, cur.ASNs[i].Nodes)
		assert.InDelta(t, w.Share, cur.ASNs[i].Share, delta)
		assert.InDelta(t, w.ConnectionShare, cur.ASNs[i].ConnectionShare, delta)
	}

	require.NotNil(t, cur.Versions)
	assert.Equal(t, 30, cur.Versions.Population)
	sum := 0.0
	for _, s := range cur.Versions.Shares {
		sum += s
	}
	assert.InDelta(t, 1, sum, delta)
	assert.InDelta(t, 20.0/30, cur.Versions.Shares["0.38.22"], delta)
	assert.InDelta(t, 6.0/30, cur.Versions.Shares["0.38.17"], delta)
	assert.InDelta(t, 4.0/30, cur.Versions.Shares[OtherVersion], delta, "a 4-node version is folded into other")
	assert.NotContains(t, cur.Versions.Shares, "0.37.6")

	require.NotNil(t, cur.AppVersions)
	assert.Equal(t, 28, cur.AppVersions.Population, "responders with a reported app version")
	assert.InDelta(t, 24.0/28, cur.AppVersions.Shares["25.1.0"], delta, "v-prefixed strings merge")
	assert.InDelta(t, 4.0/28, cur.AppVersions.Shares[OtherVersion], delta, "a 3-node version and garbage fold")
	assert.Len(t, cur.AppVersions.Shares, 2)
	assert.InDelta(t, 24.0/28, line.AppVersionShares["25.1.0"], delta)

	require.NotNil(t, cur.Graph)
	assert.Equal(t, 30, cur.Graph.Population)
	assert.InDelta(t, 0.9, cur.Graph.LargestComponentFraction, delta)
	assert.Equal(t, 5, cur.Graph.TopN)
	assert.InDelta(t, 0.55, cur.Graph.TopNShare, delta)

	require.Len(t, cur.Directory, 2)
	assert.Equal(t, "http://192.0.2.1:26657", cur.Directory[0].Endpoint)
	assert.Equal(t, "https://rpc.example.test:443", cur.Directory[1].Endpoint, "host lower-cased, trailing slash dropped")

	assert.True(t, line.At.Equal(at))
	assert.Equal(t, "test-1", line.Chain)
	assert.Equal(t, 12, line.PublicNodes)
	assert.Equal(t, 18, line.NonPublicNodes)
	assert.InDelta(t, 20.0/30, line.VersionShares["0.38.22"], delta)
	require.NotNil(t, line.LargestComponentFraction)
	assert.InDelta(t, 0.9, *line.LargestComponentFraction, delta)
	require.NotNil(t, line.TopNShare)
	assert.InDelta(t, 0.55, *line.TopNShare, delta)
}

func TestBuildWithholdsBelowFloor(t *testing.T) {
	p := DefaultParams
	p.PopulationFloor = 31 // one above the sample's population
	cur, line := Build(sample(), "test-1", at, p)
	assert.Nil(t, cur.Versions)
	assert.Nil(t, cur.AppVersions)
	assert.Nil(t, cur.Graph)
	assert.Nil(t, line.VersionShares)
	assert.Nil(t, line.AppVersionShares)
	assert.Nil(t, line.LargestComponentFraction)
	assert.Nil(t, line.TopNShare)
	assert.Len(t, cur.Countries, 4, "the floor does not touch ungated data")
	assert.Len(t, cur.Directory, 2)
	require.Len(t, cur.ASNs, 3, "ASN rows stay: they are k-gated, not floored")
	for _, row := range cur.ASNs {
		assert.Zero(t, row.ConnectionShare, "connection share goes with the graph")
	}
	raw, err := json.Marshal(cur)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "connection_share")
}

func TestBuildFloorBoundary(t *testing.T) {
	p := DefaultParams
	p.PopulationFloor = 30 // exactly the population: publish
	cur, _ := Build(sample(), "test-1", at, p)
	assert.NotNil(t, cur.Versions)
	assert.NotNil(t, cur.Graph)
}

func TestASNTableIsCappedAndDeterministic(t *testing.T) {
	r := sample()
	r.ASNs = map[uint32]crawl.ASNCount{}
	for i := uint32(1); i <= 25; i++ {
		r.ASNs[i] = crawl.ASNCount{Org: "x", Nodes: 5} // all tie at k
	}
	r.PublicNodes, r.NonPublicNodes = 125, 0
	cur, _ := Build(r, "test-1", at, DefaultParams)
	require.Len(t, cur.ASNs, DefaultParams.MaxASNRows)
	for i, row := range cur.ASNs {
		assert.Equal(t, uint32(i+1), row.ASN, "ties order by ASN ascending")
	}
	again, _ := Build(r, "test-1", at, DefaultParams)
	assert.Equal(t, cur.ASNs, again.ASNs)
}

func TestVersionBucketing(t *testing.T) {
	cases := []struct{ in, want string }{
		{"0.38.22", "0.38.22"},
		{"v0.38.22", "0.38.22"},
		{"0.34.29", "0.34.29"},
		{"1.0.1", "1.0.1"},
		{"0.0.1", OtherVersion},
		{"0.33.9", OtherVersion},
		{"0.38.22-rc1", OtherVersion},
		{"0.38", OtherVersion},
		{"", OtherVersion},
		{"garbage", OtherVersion},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			assert.Equal(t, c.want, bucketVersion(c.in))
		})
	}

	r := sample()
	r.Versions = map[string]int{"0.38.22": 20, "v0.38.22": 4, "0.0.1": 3, "": 2, "0.33.0": 1}
	cur, _ := Build(r, "test-1", at, DefaultParams)
	require.NotNil(t, cur.Versions)
	assert.Equal(t, 30, cur.Versions.Population, "every reported node counts toward the floor")
	assert.Len(t, cur.Versions.Shares, 2)
	assert.InDelta(t, 24.0/30, cur.Versions.Shares["0.38.22"], delta, "v-prefixed strings merge")
	assert.InDelta(t, 6.0/30, cur.Versions.Shares[OtherVersion], delta)
}

func TestPerBucketKFoldsRareVersions(t *testing.T) {
	counts := map[string]int{"1.0.0": 5, "1.0.1": 4, "1.0.2": 11}
	got := adoption(counts, bucketRelease, DefaultParams)
	require.NotNil(t, got)
	assert.Equal(t, 20, got.Population)
	assert.InDelta(t, 5.0/20, got.Shares["1.0.0"], delta, "exactly k stays")
	assert.InDelta(t, 11.0/20, got.Shares["1.0.2"], delta)
	assert.InDelta(t, 4.0/20, got.Shares[OtherVersion], delta, "below k folds")
	assert.NotContains(t, got.Shares, "1.0.1")

	// Nothing but rare versions: everything is "other", and it is published.
	rare := map[string]int{"1.0.0": 4, "1.0.1": 4, "1.0.2": 4, "1.0.3": 4, "1.0.4": 4}
	got = adoption(rare, bucketRelease, DefaultParams)
	require.NotNil(t, got)
	assert.Equal(t, map[string]float64{OtherVersion: 1}, got.Shares)
}

func TestAppVersionFloorIsItsOwnPopulation(t *testing.T) {
	r := sample()
	r.AppVersions = map[string]int{"25.1.0": 19} // one short of the floor
	cur, line := Build(r, "test-1", at, DefaultParams)
	assert.NotNil(t, cur.Versions, "the client panel has its own population of 30")
	assert.Nil(t, cur.AppVersions)
	assert.Nil(t, line.AppVersionShares)
	assert.NotNil(t, line.VersionShares)
}

func TestBucketRelease(t *testing.T) {
	cases := map[string]string{
		"v25.1.0": "25.1.0", "25.1.0": "25.1.0", "0.1.0": "0.1.0",
		"25.1.0-rc1": OtherVersion, "v25.1": OtherVersion, "": OtherVersion, "25.1.0+abcdef": OtherVersion,
	}
	for in, want := range cases {
		assert.Equal(t, want, bucketRelease(in), in)
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	cases := []struct {
		name, in, want string
		ok             bool
	}{
		{"plain", "http://192.0.2.1:26657", "http://192.0.2.1:26657", true},
		{"host lower-cased, slash trimmed", "https://RPC.Example.test:443/", "https://rpc.example.test:443", true},
		{"short path kept", "https://rpc.lavenderfive.test:443/cosmoshub", "https://rpc.lavenderfive.test:443/cosmoshub", true},
		{"nested short path", "https://public.stakewolle.test/cosmos/cosmoshub/rpc", "https://public.stakewolle.test/cosmos/cosmoshub/rpc", true},
		{"ipv6", "http://[2001:db8::1]:26657", "http://[2001:db8::1]:26657", true},
		{"token path dropped", "https://go.getblock.test/17515cb3ec0e43b7817f182e5de6066a", "", false},
		{"token in nested path dropped", "https://x.test/v1/AbCdEfGhIjKlMnOpQrStUv/rpc", "", false},
		{"jwt-shaped token dropped", "https://x.test/eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc/rpc", "", false},
		{"percent-encoded long segment dropped", "https://x.test/a%20very%20long%20segment%20here", "", false},
		{"nineteen-char segment kept", "https://x.test/" + strings.Repeat("a", 19), "https://x.test/" + strings.Repeat("a", 19), true},
		{"query dropped", "https://rpc.test/?key=abc", "", false},
		{"bare query dropped", "https://rpc.test/?", "", false},
		{"userinfo dropped", "https://user:pw@rpc.test", "", false},
		{"fragment dropped", "https://rpc.test/#x", "", false},
		{"bad scheme", "tcp://rpc.test:26657", "", false},
		{"no host", "http:///path", "", false},
		{"garbage", "::not a url", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := normalizeEndpoint(c.in)
			assert.Equal(t, c.ok, ok)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestDirectoryDropsUnpublishableEndpointsButKeepsCounts(t *testing.T) {
	r := sample()
	r.Directory = append(r.Directory,
		model.NodeRecord{Endpoint: "https://go.getblock.test/17515cb3ec0e43b7817f182e5de6066a"},
		model.NodeRecord{Endpoint: "https://rpc.test/?key=secret"},
		model.NodeRecord{Endpoint: "http://192.0.2.1:26657/"}, // normalizes to an existing one
	)
	cur, _ := Build(r, "test-1", at, DefaultParams)
	require.Len(t, cur.Directory, 2)
	raw, err := json.Marshal(cur)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "getblock")
	assert.NotContains(t, string(raw), "secret")
	assert.Equal(t, 12, cur.PublicNodes, "dropping an endpoint never changes a count")
}

func TestEmptyResultProducesEmptyNotNull(t *testing.T) {
	cur, line := Build(&crawl.Result{}, "test-1", at, DefaultParams)
	raw, err := json.Marshal(cur)
	require.NoError(t, err)
	for _, want := range []string{`"countries":{}`, `"asns":[]`, `"directory":[]`} {
		assert.Contains(t, string(raw), want)
	}
	for _, forbid := range []string{`"versions"`, `"app_versions"`, `"graph"`} {
		assert.NotContains(t, string(raw), forbid, "floored panels must be absent, not null")
	}
	rawLine, err := json.Marshal(line)
	require.NoError(t, err)
	assert.NotContains(t, string(rawLine), `"version_shares"`)
	assert.NotContains(t, string(rawLine), `"app_version_shares"`)
	assert.NotContains(t, string(rawLine), `"largest_component_fraction"`)
	assert.NotContains(t, string(rawLine), `"top_n_share"`)
	assert.Contains(t, string(rawLine), `"schema_version":1`)
}

func TestBuildDoesNotAliasInputs(t *testing.T) {
	r := sample()
	cur, _ := Build(r, "test-1", at, DefaultParams)
	r.Countries["DE"] = 0
	r.Directory[0].Endpoint = "changed"
	assert.Equal(t, 14, cur.Countries["DE"])
	assert.Equal(t, "http://192.0.2.1:26657", cur.Directory[0].Endpoint)
}

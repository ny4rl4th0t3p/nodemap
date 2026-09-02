package geo

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/oschwald/maxminddb-golang/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/nodemap/internal/crawl"
)

// Both backends must satisfy the crawler's interface.
var (
	_ crawl.Enricher = None{}
	_ crawl.Enricher = (*MMDB)(nil)
)

func TestNoneKnowsNothing(t *testing.T) {
	for _, ip := range []string{"192.0.2.1", "2001:db8::1", "", "garbage"} {
		assert.Equal(t, crawl.Enrichment{}, (None{}).Lookup(ip), ip)
	}
}

func TestOpenRejectsNoPaths(t *testing.T) {
	_, err := Open("", "")
	assert.ErrorIs(t, err, ErrNoDatabase)
}

func TestOpenRejectsMissingOrInvalidFiles(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.mmdb")
	empty := filepath.Join(t.TempDir(), "empty.mmdb")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	junk := filepath.Join(t.TempDir(), "junk.mmdb")
	require.NoError(t, os.WriteFile(junk, []byte("this is not a database"), 0o600))

	cases := map[string][2]string{
		"missing country": {missing, ""},
		"missing asn":     {"", missing},
		"empty country":   {empty, ""},
		"junk asn":        {"", junk},
	}
	for name, paths := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Open(paths[0], paths[1])
			assert.Error(t, err)
		})
	}
}

func TestLookupWithoutDatabasesIsUnknownAndSafe(t *testing.T) {
	m := &MMDB{}
	for _, ip := range []string{"192.0.2.1", "::ffff:192.0.2.1", "", "not-an-ip", "300.1.1.1"} {
		assert.Equal(t, crawl.Enrichment{}, m.Lookup(ip), ip)
	}
	assert.NoError(t, m.Close())
	assert.NoError(t, m.Close(), "second close")
}

// TestSmokeRealDatabases runs only when the operator points it at real
// files. It prints what the databases say about one IP and the raw record
// keys, so the struct tags above can be checked against the actual schema.
//
//	NODEMAP_GEO_COUNTRY=/path/dbip-country-lite.mmdb \
//	NODEMAP_GEO_ASN=/path/dbip-asn-lite.mmdb \
//	NODEMAP_GEO_IP=1.1.1.1 go test -run TestSmokeRealDatabases -v ./internal/geo/
func TestSmokeRealDatabases(t *testing.T) {
	countryPath, asnPath := os.Getenv("NODEMAP_GEO_COUNTRY"), os.Getenv("NODEMAP_GEO_ASN")
	if countryPath == "" || asnPath == "" {
		t.Skip("set NODEMAP_GEO_COUNTRY and NODEMAP_GEO_ASN to run against real databases")
	}
	ip := os.Getenv("NODEMAP_GEO_IP")
	if ip == "" {
		ip = "1.1.1.1"
	}
	m, err := Open(countryPath, asnPath)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, m.Close()) })

	got := m.Lookup(ip)
	t.Logf("%s => %+v", ip, got)
	assert.NotEmpty(t, got.Country, "check the country schema")
	assert.NotZero(t, got.ASN, "check the asn schema")
	assert.NotEmpty(t, got.ASOrg, "check the asn schema")

	addr := netip.MustParseAddr(ip)
	for name, r := range map[string]*maxminddb.Reader{"country": m.country, "asn": m.asn} {
		var raw map[string]any
		require.NoError(t, r.Lookup(addr).Decode(&raw), name)
		t.Logf("%s database %q built %s: raw record %v",
			name, r.Metadata.DatabaseType, r.Metadata.BuildTime().UTC().Format("2006-01-02"), raw)
	}

	// Documentation ranges and loopback must not resolve to anything.
	for _, reserved := range []string{"192.0.2.1", "127.0.0.1"} {
		assert.Equal(t, crawl.Enrichment{}, m.Lookup(reserved), reserved)
	}
}

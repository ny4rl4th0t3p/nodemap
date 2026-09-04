package model

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// forbiddenAnywhere are substrings that must not appear in any field name or
// JSON tag of any persisted type, at any depth. Each maps to a denylist rule
// in the security boundary: peer edges, node identity, validator identity,
// third-party IPs, and infrastructure-movement history.
var forbiddenAnywhere = []string{
	"nodeid", "node_id",
	"pubkey", "pub_key",
	"validator",
	"votingpower", "voting_power",
	"remoteip", "remote_ip",
	"peer",
	"edge",
	"outbound",
	"consensus",
	"iphistory", "ip_history",
	"moniker",
}

// forbiddenOnNodeRecord are substrings that may appear in aggregates but
// never on an individual record: a per-node build is a targeting list, and a
// per-node IP is the raw material for a movement trail.
var forbiddenOnNodeRecord = []string{"version", "ip", "history", "addr"}

// allowedNodeRecordFields is the complete field set of NodeRecord. Adding a
// field means editing this list deliberately and justifying it against the
// field-by-field classification.
var allowedNodeRecordFields = []string{
	"Endpoint", "Country", "ASN", "ASOrg",
	"EarliestBlockHeight", "TxIndex", "CatchingUp",
}

// persisted lists every type that reaches disk. Anything new that is written
// must be added here so the walks cover it.
var persisted = []any{Current{}, HistoryLine{}, DirectoryState{}}

// allowedJSONKeys is the complete set of JSON keys that can appear in any
// persisted file. The README's field policy is written against this list;
// a new key here is a new published field and needs the same justification.
var allowedJSONKeys = map[string][]string{
	"Current": {
		"schema_version", "chain", "crawled_at", "public_nodes", "non_public_nodes",
		"countries",
		"asns", "asn", "org", "nodes", "share", "connection_share",
		"versions", "population", "shares",
		"graph", "largest_component_fraction", "top_n", "top_n_share",
		"directory", "endpoint", "country", "as_org",
		"earliest_block_height", "tx_index", "catching_up",
	},
	"HistoryLine": {
		"schema_version", "at", "chain", "public_nodes", "non_public_nodes",
		"version_shares", "largest_component_fraction", "top_n_share",
	},
	"DirectoryState": {"schema_version", "endpoints"},
}

func TestNoForbiddenFieldsAnywhere(t *testing.T) {
	for _, v := range persisted {
		rt := reflect.TypeOf(v)
		t.Run(rt.Name(), func(t *testing.T) {
			walk(rt, rt.Name(), map[reflect.Type]bool{}, func(path, name, tag string) {
				for _, bad := range forbiddenAnywhere {
					assert.NotContains(t, strings.ToLower(name), bad, "%s.%s", path, name)
					assert.NotContains(t, strings.ToLower(tag), bad, "%s.%s tag", path, name)
				}
			})
		})
	}
}

func TestPersistedJSONKeysAreExactlyTheAllowlist(t *testing.T) {
	for _, v := range persisted {
		rt := reflect.TypeOf(v)
		t.Run(rt.Name(), func(t *testing.T) {
			want, ok := allowedJSONKeys[rt.Name()]
			require.True(t, ok, "no allowlist for persisted type %s", rt.Name())
			got := map[string]bool{}
			walk(rt, rt.Name(), map[reflect.Type]bool{}, func(_, _, tag string) {
				if key := strings.Split(tag, ",")[0]; key != "" {
					got[key] = true
				}
			})
			keys := make([]string, 0, len(got))
			for k := range got {
				keys = append(keys, k)
			}
			assert.ElementsMatch(t, want, keys)
		})
	}
}

func TestNodeRecordFieldsAreExactlyTheAllowlist(t *testing.T) {
	rt := reflect.TypeOf(NodeRecord{})
	var got []string
	for i := range rt.NumField() {
		f := rt.Field(i)
		got = append(got, f.Name)
		for _, bad := range forbiddenOnNodeRecord {
			assert.NotContains(t, strings.ToLower(f.Name), bad, "NodeRecord.%s", f.Name)
		}
		switch f.Type.Kind() {
		case reflect.Struct, reflect.Slice, reflect.Map, reflect.Pointer:
			t.Errorf("NodeRecord.%s is a %s; individual records hold scalars only", f.Name, f.Type.Kind())
		default:
		}
	}
	assert.Equal(t, allowedNodeRecordFields, got)
}

func TestHistoryLineHasNoPerNodeContent(t *testing.T) {
	rt := reflect.TypeOf(HistoryLine{})
	timeType := reflect.TypeOf(HistoryLine{}.At)
	for i := range rt.NumField() {
		f := rt.Field(i)
		switch f.Type.Kind() {
		case reflect.Slice, reflect.Struct:
			assert.Equal(t, timeType, f.Type, "HistoryLine.%s: a history line carries counters only", f.Name)
		case reflect.Map:
			elem := f.Type.Elem().Kind()
			assert.True(t, elem == reflect.Float64 || elem == reflect.Int,
				"HistoryLine.%s maps to %s; only numeric aggregates are allowed", f.Name, f.Type.Elem())
		default:
			// Scalars and pointers to scalars are what a history line is made of.
		}
	}
}

func TestDirectoryStateHoldsEndpointsAndTimesOnly(t *testing.T) {
	rt := reflect.TypeOf(DirectoryState{})
	require.Equal(t, 2, rt.NumField())
	assert.Equal(t, reflect.Int, rt.Field(0).Type.Kind(), "schema version")
	f := rt.Field(1)
	require.Equal(t, reflect.Map, f.Type.Kind())
	assert.Equal(t, reflect.String, f.Type.Key().Kind())
	assert.Equal(t, reflect.TypeOf(HistoryLine{}.At), f.Type.Elem())
}

func TestEveryPersistedTypeCarriesTheSchemaVersion(t *testing.T) {
	for _, v := range persisted {
		rt := reflect.TypeOf(v)
		f, ok := rt.FieldByName("SchemaVersion")
		require.True(t, ok, "%s has no SchemaVersion", rt.Name())
		assert.Equal(t, "schema_version", f.Tag.Get("json"), rt.Name())
	}
	assert.Equal(t, 1, SchemaVersion)
}

func TestVersionAdoptionHasNoJoinKey(t *testing.T) {
	rt := reflect.TypeOf(VersionAdoption{})
	for i := range rt.NumField() {
		lower := strings.ToLower(rt.Field(i).Name)
		for _, key := range []string{"country", "asn", "endpoint", "city", "org"} {
			assert.NotContains(t, lower, key, "VersionAdoption.%s would let version be cross-tabbed", rt.Field(i).Name)
		}
	}
}

// walk visits every struct field reachable from rt, following pointers,
// slices, arrays, and map values, and calls visit with the dotted path.
func walk(rt reflect.Type, path string, seen map[reflect.Type]bool, visit func(path, name, tag string)) {
	switch rt.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		walk(rt.Elem(), path, seen, visit)
		return
	case reflect.Map:
		walk(rt.Key(), path+"[key]", seen, visit)
		walk(rt.Elem(), path+"[value]", seen, visit)
		return
	case reflect.Struct:
	default:
		return
	}
	if seen[rt] || rt.PkgPath() == "time" {
		return
	}
	seen[rt] = true
	for i := range rt.NumField() {
		f := rt.Field(i)
		visit(path, f.Name, f.Tag.Get("json"))
		walk(f.Type, path+"."+f.Name, seen, visit)
	}
}

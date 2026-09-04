package rpc

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixture(tb testing.TB, name string) []byte {
	tb.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(tb, err)
	return data
}

func TestDecodeNetInfoFixture(t *testing.T) {
	ni, err := DecodeNetInfo(bytes.NewReader(fixture(t, "net_info.json")))
	require.NoError(t, err)
	require.Len(t, ni.Peers, 10)
	p := ni.Peers[0]
	assert.Equal(t, "192.0.2.10", p.RemoteIP)
	assert.Equal(t, "192.0.2.10:26656", p.NodeInfo.ListenAddr)
	assert.Equal(t, "cosmoshub-4", p.NodeInfo.Network)
	assert.Equal(t, "0.38.22", p.NodeInfo.Version)
	assert.Equal(t, "on", p.NodeInfo.Other.TxIndex)
	assert.Equal(t, "tcp://0.0.0.0:26657", p.NodeInfo.Other.RPCAddress)

	networks := map[string]int{}
	for _, p := range ni.Peers {
		networks[p.NodeInfo.Network]++
	}
	assert.Equal(t, map[string]int{"cosmoshub-4": 9, "osmosis-1": 1}, networks)
	assert.Equal(t, "2001:db8::1", ni.Peers[9].RemoteIP)
	assert.Empty(t, ni.Peers[5].NodeInfo.Other.RPCAddress)
}

func TestDecodeStatusFixture(t *testing.T) {
	st, err := DecodeStatus(bytes.NewReader(fixture(t, "status.json")))
	require.NoError(t, err)
	assert.Equal(t, "1111111111111111111111111111111111111111", st.NodeInfo.ID)
	assert.Equal(t, "cosmoshub-4", st.NodeInfo.Network)
	assert.Equal(t, "192.0.2.1:26656", st.NodeInfo.ListenAddr)
	assert.Equal(t, "tcp://0.0.0.0:26657", st.NodeInfo.Other.RPCAddress)
	assert.Equal(t, int64(30000000), st.SyncInfo.LatestBlockHeight)
	assert.Equal(t, int64(1), st.SyncInfo.EarliestBlockHeight)
	assert.False(t, st.SyncInfo.CatchingUp)
	assert.True(t, st.Validator, "the fixture reports voting power 1000")
}

// TestStatusFixtureCarriesValidatorIdentityAndTypeDropsIt proves the test
// is meaningful: the fixture really contains a validator address and key,
// and the decoded type has nowhere to put them. The one thing kept is a
// boolean.
func TestStatusFixtureCarriesValidatorIdentityAndTypeDropsIt(t *testing.T) {
	raw := fixture(t, "status.json")
	for _, key := range []string{`"validator_info"`, `"address"`, `"pub_key"`, `"voting_power"`} {
		require.Contains(t, string(raw), key, "fixture must carry %s or the test is vacuous", key)
	}
	assertNoField(t, reflect.TypeOf(Status{}), "pub_key", "pubkey", "voting", "consensus")
	f, ok := reflect.TypeOf(Status{}).FieldByName("Validator")
	require.True(t, ok)
	assert.Equal(t, reflect.Bool, f.Type.Kind(), "validator_info reduces to a boolean and nothing else")
	assert.Equal(t, "-", f.Tag.Get("json"), "and that boolean is never serialized")
}

func TestStatusValidatorFlag(t *testing.T) {
	cases := map[string]struct {
		body string
		want bool
	}{
		"positive":    {`{"result":{"validator_info":{"voting_power":"1485339"}}}`, true},
		"one":         {`{"result":{"validator_info":{"voting_power":"1"}}}`, true},
		"zero":        {`{"result":{"validator_info":{"voting_power":"0"}}}`, false},
		"negative":    {`{"result":{"validator_info":{"voting_power":"-5"}}}`, false},
		"garbage":     {`{"result":{"validator_info":{"voting_power":"lots"}}}`, false},
		"empty":       {`{"result":{"validator_info":{"voting_power":""}}}`, false},
		"absent":      {`{"result":{"node_info":{}}}`, false},
		"wrong shape": {`{"result":{"validator_info":"x"}}`, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			st, err := DecodeStatus(strings.NewReader(c.body))
			if name == "wrong shape" {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.want, st.Validator)
		})
	}
}

func TestDecodeABCIInfoKeepsOnlyTheVersion(t *testing.T) {
	raw := fixture(t, "abci_info.json")
	for _, key := range []string{`"data"`, `"app_version"`, `"last_block_height"`, `"last_block_app_hash"`} {
		require.Contains(t, string(raw), key, "fixture must carry %s or the drop test is vacuous", key)
	}
	ai, err := DecodeABCIInfo(bytes.NewReader(raw))
	require.NoError(t, err)
	assert.Equal(t, "v25.1.0", ai.Response.Version)
	assertNoField(t, reflect.TypeOf(ABCIInfo{}), "data", "app_version", "hash", "height")
	rt := reflect.TypeOf(ai.Response)
	assert.Equal(t, 1, rt.NumField(), "the response holds the version string and nothing else")
}

func TestNetInfoTypeDropsDirectionStatsAndPeerID(t *testing.T) {
	raw := fixture(t, "net_info.json")
	for _, key := range []string{`"is_outbound"`, `"connection_status"`, `"id"`} {
		require.Contains(t, string(raw), key, "fixture must carry %s or the test is vacuous", key)
	}
	require.Contains(t, string(raw), `"moniker"`)
	assertNoField(t, reflect.TypeOf(NetInfo{}), "outbound", "connection", "channels", "protocol", "moniker", "id")
}

// assertNoField walks rt and fails if any field name or JSON tag contains
// one of the substrings (case-insensitive). "id" is matched as a whole tag
// or name so that fields like "tx_index" are not caught by accident.
func assertNoField(t *testing.T, rt reflect.Type, bad ...string) {
	t.Helper()
	seen := map[reflect.Type]bool{}
	var walk func(rt reflect.Type, path string)
	walk = func(rt reflect.Type, path string) {
		switch rt.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			walk(rt.Elem(), path)
			return
		case reflect.Struct:
		default:
			return
		}
		if seen[rt] {
			return
		}
		seen[rt] = true
		for i := range rt.NumField() {
			f := rt.Field(i)
			name := strings.ToLower(f.Name)
			tag := strings.ToLower(strings.Split(f.Tag.Get("json"), ",")[0])
			for _, b := range bad {
				hit := strings.Contains(name, b) || strings.Contains(tag, b)
				if b == "id" {
					hit = name == b || tag == b
				}
				assert.False(t, hit, "%s.%s (tag %q) matches forbidden %q", path, f.Name, tag, b)
			}
			walk(f.Type, path+"."+f.Name)
		}
	}
	walk(rt, rt.Name())
}

func TestDecodeEnvelopeErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
		want error
	}{
		{"rpc error", `{"jsonrpc":"2.0","id":-1,"error":{"code":-32603,"message":"boom"}}`, ErrRPCError},
		{"empty result", `{"jsonrpc":"2.0","id":-1}`, ErrEmptyResult},
		{"too large", strings.Repeat("x", MaxBody+1), ErrBodyTooLarge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := DecodeNetInfo(strings.NewReader(c.body))
			assert.ErrorIs(t, err, c.want)
		})
	}
	malformed := []string{``, `{`, `[]`, `{"result":5}`, `{"result":{"peers":5}}`, `{"result":{"peers":[{"remote_ip":1}]}}`}
	for _, body := range malformed {
		t.Run(body, func(t *testing.T) {
			_, err := DecodeNetInfo(strings.NewReader(body))
			assert.Error(t, err)
		})
	}
	_, err := DecodeStatus(strings.NewReader(`{"result":{"sync_info":{"latest_block_height":"abc"}}}`))
	assert.Error(t, err, "non-numeric height")
}

func TestErrorMessageIsTruncated(t *testing.T) {
	long := strings.Repeat("m", maxErrorMessage*3)
	_, err := DecodeNetInfo(strings.NewReader(`{"error":{"code":1,"message":"` + long + `"}}`))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), long)
}

func FuzzDecodeNetInfo(f *testing.F) {
	f.Add(fixture(f, "net_info.json"))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"result":{"peers":[]}}`))
	f.Add([]byte(`{"result":{"peers":[{"node_info":{"other":{}}}]}}`))
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = DecodeNetInfo(bytes.NewReader(data))
	})
}

func FuzzDecodeABCIInfo(f *testing.F) {
	f.Add(fixture(f, "abci_info.json"))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"result":{"response":{}}}`))
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = DecodeABCIInfo(bytes.NewReader(data))
	})
}

func FuzzDecodeStatus(f *testing.F) {
	f.Add(fixture(f, "status.json"))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"result":{"node_info":{},"sync_info":{}}}`))
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = DecodeStatus(bytes.NewReader(data))
	})
}

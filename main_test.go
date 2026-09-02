package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/nodemap/internal/model"
	"github.com/ny4rl4th0t3p/nodemap/internal/output"
	"github.com/ny4rl4th0t3p/nodemap/internal/rpc"
	"github.com/ny4rl4th0t3p/nodemap/internal/suppress"
)

const testChain = "test-1"

// node is one synthetic node: its id and the JSON array of its peers.
type node struct {
	id, peers string
}

// world serves synthetic nodes in-process, keyed by host:port.
type world map[string]node

func (w world) RoundTrip(req *http.Request) (*http.Response, error) {
	n, ok := w[req.URL.Host]
	if !ok {
		return nil, errors.New("dial tcp: connection refused")
	}
	rec := httptest.NewRecorder()
	switch req.URL.Path {
	case "/status":
		_, _ = fmt.Fprintf(rec, `{"result":{"node_info":{"id":%q,"listen_addr":"tcp://0.0.0.0:26656","network":%q,`+
			`"version":"0.38.22","moniker":"m","other":{"tx_index":"on","rpc_address":"tcp://0.0.0.0:26657"}},`+
			`"sync_info":{"latest_block_height":"1","earliest_block_height":"1","catching_up":false},`+
			`"validator_info":{"address":"AA","voting_power":"0"}}}`, n.id, testChain)
	case "/net_info":
		_, _ = fmt.Fprintf(rec, `{"result":{"peers":[%s]}}`, n.peers)
	default:
		rec.WriteHeader(http.StatusNotFound)
	}
	return rec.Result(), nil
}

func peer(ip string) string {
	return fmt.Sprintf(`{"node_info":{"id":"p","listen_addr":"tcp://0.0.0.0:26656","network":%q,"version":"0.38.22",`+
		`"other":{"rpc_address":"tcp://0.0.0.0:26657"}},"remote_ip":%q}`, testChain, ip)
}

func twoNodes() world {
	return world{
		"192.0.2.1:26657": {id: "id-one", peers: peer("192.0.2.2") + "," + peer("192.0.2.3")},
		"192.0.2.2:26657": {id: "id-two", peers: peer("192.0.2.1")},
	}
}

func writeSeeds(t *testing.T, chainID string, addresses ...string) string {
	t.Helper()
	sf := seedFile{ChainID: chainID}
	for _, a := range addresses {
		sf.APIs.RPC = append(sf.APIs.RPC, struct {
			Address string `json:"address"`
		}{a})
	}
	data, err := json.Marshal(sf)
	require.NoError(t, err)
	p := filepath.Join(t.TempDir(), "seeds.json")
	require.NoError(t, os.WriteFile(p, data, 0o600))
	return p
}

func testConfig(t *testing.T, seeds string) config {
	t.Helper()
	return config{
		seeds:      seeds,
		chain:      testChain,
		out:        filepath.Join(t.TempDir(), "out"),
		workers:    2,
		timeout:    time.Second,
		maxRuntime: 10 * time.Second,
		progress:   time.Millisecond,
		downWindow: 24 * time.Hour,
	}
}

func newClient() *rpc.Client { return rpc.NewClientWithTransport(time.Second, twoNodes()) }

func readCurrent(t *testing.T, dir string) (cur model.Current, raw []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, output.CurrentFile))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &cur))
	return cur, raw
}

func TestRunWritesAllFiles(t *testing.T) {
	cfg := testConfig(t, writeSeeds(t, testChain, "http://192.0.2.1:26657"))
	var stderr bytes.Buffer
	for i := range 2 {
		require.NoError(t, run(context.Background(), cfg, newClient(), &stderr), "run %d", i)
	}
	cur, raw := readCurrent(t, cfg.out)
	assert.Equal(t, testChain, cur.Chain)
	assert.Equal(t, 2, cur.PublicNodes)
	assert.Equal(t, 1, cur.NonPublicNodes)
	assert.Len(t, cur.Directory, 2)
	assert.Nil(t, cur.Versions, "thin chain publishes no versions")
	assert.Nil(t, cur.Graph, "thin chain publishes no graph")
	for _, forbidden := range []string{"id-", "validator", "voting", "192.0.2.3"} {
		assert.NotContains(t, string(raw), forbidden)
	}

	hist, err := os.ReadFile(filepath.Join(cfg.out, output.HistoryFile))
	require.NoError(t, err)
	assert.Equal(t, 2, bytes.Count(hist, []byte("\n")), "one history line per run")

	stateRaw, err := os.ReadFile(filepath.Join(cfg.out, output.DirectoryStateFile))
	require.NoError(t, err)
	var state model.DirectoryState
	require.NoError(t, json.Unmarshal(stateRaw, &state))
	assert.Len(t, state.Endpoints, 2)
	assert.Contains(t, state.Endpoints, "http://192.0.2.1:26657")
	assert.Contains(t, state.Endpoints, "http://192.0.2.2:26657")

	assert.Contains(t, stderr.String(), "public=2 non_public=1")
	assert.Contains(t, stderr.String(), "pending=0", "final progress line")
	assert.NotContains(t, stderr.String(), "192.0.2")
	assert.NotContains(t, stderr.String(), "id-")
}

func TestRunFailsWithoutWritingWhenNoSeedAnswers(t *testing.T) {
	cfg := testConfig(t, writeSeeds(t, testChain, "http://192.0.2.9:26657", "not a url"))
	err := run(context.Background(), cfg, newClient(), &bytes.Buffer{})
	assert.ErrorIs(t, err, errNoSeeds)
	assert.NoDirExists(t, cfg.out, "a failed run must not create output")
}

func TestRunStoppedEarlyFailsWithoutWriting(t *testing.T) {
	cfg := testConfig(t, writeSeeds(t, testChain, "http://192.0.2.1:26657"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, run(ctx, cfg, newClient(), &bytes.Buffer{}), errIncomplete)
	assert.NoDirExists(t, cfg.out)

	cfg.maxRuntime = time.Nanosecond
	assert.ErrorIs(t, run(context.Background(), cfg, newClient(), &bytes.Buffer{}), errIncomplete)
	assert.NoDirExists(t, cfg.out)
}

func TestRunHonorsSuppressionList(t *testing.T) {
	const salt = "s"
	list := filepath.Join(t.TempDir(), "suppression.txt")
	require.NoError(t, os.WriteFile(list, []byte(suppress.Hash(salt, "id-two")+"\n"), 0o600))
	cfg := testConfig(t, writeSeeds(t, testChain, "http://192.0.2.1:26657"))
	cfg.suppress, cfg.salt = list, salt
	require.NoError(t, run(context.Background(), cfg, newClient(), &bytes.Buffer{}))
	cur, _ := readCurrent(t, cfg.out)
	assert.Equal(t, 2, cur.PublicNodes, "a suppressed node still counts")
	require.Len(t, cur.Directory, 1, "a suppressed node is not listed")
	assert.Equal(t, "http://192.0.2.1:26657", cur.Directory[0].Endpoint)
}

func TestRunRejectsBadInputs(t *testing.T) {
	cases := map[string]func(t *testing.T, c *config){
		"missing seeds file": func(t *testing.T, c *config) {
			c.seeds = filepath.Join(t.TempDir(), "missing.json")
		},
		"empty seeds": func(t *testing.T, c *config) {
			c.seeds = writeSeeds(t, testChain)
		},
		"seeds for another chain": func(t *testing.T, c *config) {
			c.seeds = writeSeeds(t, "other-1", "http://192.0.2.1:26657")
		},
		"zero workers": func(_ *testing.T, c *config) {
			c.workers = 0
		},
		"negative down window": func(_ *testing.T, c *config) {
			c.downWindow = -time.Hour
		},
		"missing geo database": func(t *testing.T, c *config) {
			c.geoCountry = filepath.Join(t.TempDir(), "x.mmdb")
		},
		"missing suppression list": func(t *testing.T, c *config) {
			c.suppress = filepath.Join(t.TempDir(), "x.txt")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t, writeSeeds(t, testChain, "http://192.0.2.1:26657"))
			mutate(t, &cfg)
			assert.Error(t, run(context.Background(), cfg, newClient(), &bytes.Buffer{}))
			assert.NoDirExists(t, cfg.out)
		})
	}
}

func TestLoadSeedsAcceptsFileWithoutChainID(t *testing.T) {
	seeds, err := loadSeeds(writeSeeds(t, "", "http://192.0.2.1:26657", "", "http://192.0.2.2:26657"), testChain)
	require.NoError(t, err)
	assert.Equal(t, []string{"http://192.0.2.1:26657", "http://192.0.2.2:26657"}, seeds, "empty addresses are skipped")
}

func TestHashMode(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, hashMode(strings.NewReader("ABC\n"), &out, "salt"))
	assert.Equal(t, suppress.Hash("salt", "abc"), strings.TrimSpace(out.String()))
	assert.Error(t, hashMode(strings.NewReader("abc"), &bytes.Buffer{}, ""), "no salt")
	assert.Error(t, hashMode(strings.NewReader(""), &bytes.Buffer{}, "salt"), "empty stdin")
	assert.Error(t, hashMode(strings.NewReader("  \n"), &bytes.Buffer{}, "salt"), "blank stdin")
}

func TestParseFlagsDefaults(t *testing.T) {
	t.Setenv(saltEnvVar, "from-env")
	cfg := parseFlags([]string{"-chain", "x-1", "-workers", "3"})
	assert.Equal(t, "x-1", cfg.chain)
	assert.Equal(t, 3, cfg.workers)
	assert.Equal(t, "from-env", cfg.salt)
	assert.Equal(t, defaultSeeds, cfg.seeds)
	assert.Equal(t, defaultTimeout, cfg.timeout)
	assert.Equal(t, defaultMaxRuntime, cfg.maxRuntime)
	assert.Equal(t, defaultProgress, cfg.progress)
	assert.Equal(t, defaultDownWindow, cfg.downWindow)
	assert.False(t, cfg.hash)
}

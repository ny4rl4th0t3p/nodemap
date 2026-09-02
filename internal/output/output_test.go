package output

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/nodemap/internal/model"
)

var at = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

func snapshot(public int) *model.Current {
	return &model.Current{
		Aggregates: model.Aggregates{
			Chain: "test-1", CrawledAt: at, PublicNodes: public, NonPublicNodes: 3,
			Countries: map[string]int{"DE": 2}, ASNs: []model.ASNShare{},
		},
		Directory: []model.NodeRecord{{Endpoint: "http://192.0.2.1:26657", Country: "DE"}},
	}
}

func TestWriteCurrentReplaces(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out", "nested")
	require.NoError(t, WriteCurrent(dir, snapshot(1)))
	require.NoError(t, WriteCurrent(dir, snapshot(2)))
	raw, err := os.ReadFile(filepath.Join(dir, CurrentFile))
	require.NoError(t, err)
	var got model.Current
	require.NoError(t, json.Unmarshal(raw, &got), "current.json must be valid JSON")
	assert.Equal(t, 2, got.PublicNodes)
	assert.Equal(t, "test-1", got.Chain)
	assert.Len(t, got.Directory, 1)
	assert.True(t, got.CrawledAt.Equal(at))
	assert.True(t, bytes.HasSuffix(raw, []byte("}\n")), "newline-terminated")
	assert.Contains(t, string(raw), "\n  ", "indented")
	assert.NoFileExists(t, filepath.Join(dir, CurrentFile+".tmp"))
}

func TestAppendHistoryAppends(t *testing.T) {
	dir := t.TempDir()
	for i := 1; i <= 3; i++ {
		line := &model.HistoryLine{At: at.Add(time.Duration(i) * time.Hour), Chain: "test-1", PublicNodes: i}
		require.NoError(t, AppendHistory(dir, line))
	}
	raw, err := os.ReadFile(filepath.Join(dir, HistoryFile))
	require.NoError(t, err)
	lines := bytes.Split(bytes.TrimSuffix(raw, []byte("\n")), []byte("\n"))
	require.Len(t, lines, 3)
	for i, l := range lines {
		assert.NotContains(t, string(l), "  ", "line %d is compact", i)
		var got model.HistoryLine
		require.NoError(t, json.Unmarshal(l, &got))
		assert.Equal(t, i+1, got.PublicNodes)
	}
}

func TestUpdateDirectoryState(t *testing.T) {
	dir := t.TempDir()
	window := 24 * time.Hour

	// Fresh: no previous file.
	s1, err := UpdateDirectoryState(dir, at, []string{"a", "b"}, window)
	require.NoError(t, err)
	assert.Equal(t, map[string]time.Time{"a": at, "b": at}, s1.Endpoints)

	// An hour later b is gone from the live set but stays as recently seen.
	s2, err := UpdateDirectoryState(dir, at.Add(time.Hour), []string{"a", "c"}, window)
	require.NoError(t, err)
	assert.Equal(t, map[string]time.Time{"a": at.Add(time.Hour), "b": at, "c": at.Add(time.Hour)}, s2.Endpoints)

	// Past the window b is pruned; a and c are refreshed.
	later := at.Add(window + 2*time.Hour)
	s3, err := UpdateDirectoryState(dir, later, []string{"a", "c"}, window)
	require.NoError(t, err)
	assert.Equal(t, map[string]time.Time{"a": later, "c": later}, s3.Endpoints)

	// The file on disk is the returned state, indented, newline-terminated.
	raw, err := os.ReadFile(filepath.Join(dir, DirectoryStateFile))
	require.NoError(t, err)
	var onDisk model.DirectoryState
	require.NoError(t, json.Unmarshal(raw, &onDisk))
	assert.Equal(t, s3, onDisk)
	assert.True(t, bytes.HasSuffix(raw, []byte("}\n")))
	assert.NoFileExists(t, filepath.Join(dir, DirectoryStateFile+".tmp"))
}

func TestUpdateDirectoryStateStartsFreshOnCorruptFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, DirectoryStateFile), []byte("{not json"), 0o600))
	s, err := UpdateDirectoryState(dir, at, []string{"a"}, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, map[string]time.Time{"a": at}, s.Endpoints)

	require.NoError(t, os.WriteFile(filepath.Join(dir, DirectoryStateFile), []byte(`{"endpoints":null}`), 0o600))
	s, err = UpdateDirectoryState(dir, at, nil, time.Hour)
	require.NoError(t, err)
	assert.Empty(t, s.Endpoints)
}

func TestWriteFailsOnUnwritableDir(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	// A regular file where the directory should be.
	assert.Error(t, WriteCurrent(file, snapshot(1)))
	assert.Error(t, AppendHistory(file, &model.HistoryLine{}))
	_, err := UpdateDirectoryState(file, at, nil, time.Hour)
	assert.Error(t, err)
}

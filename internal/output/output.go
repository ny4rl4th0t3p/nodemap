// Package output writes the persisted files with their lifecycles:
// current.json is replaced atomically on every run, history.jsonl is
// appended one line per run, and directory-state.json is rewritten from the
// previous state plus this run's live endpoints. Only model types reach this
// package, so nothing it writes can carry more than the schema allows.
package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ny4rl4th0t3p/nodemap/internal/model"
)

const (
	// CurrentFile is the replaced snapshot.
	CurrentFile = "current.json"
	// HistoryFile is the append-only series of aggregate counters.
	HistoryFile = "history.jsonl"
	// DirectoryStateFile remembers when each Tier A endpoint last answered.
	DirectoryStateFile = "directory-state.json"

	dirPerm  = 0o750
	filePerm = 0o600
)

// WriteCurrent replaces dir/current.json with cur, writing to a temporary
// file first so a crash never leaves a truncated snapshot behind.
func WriteCurrent(dir string, cur *model.Current) error {
	data, err := json.MarshalIndent(cur, "", "  ")
	if err != nil {
		return fmt.Errorf("output: encode current: %w", err)
	}
	return replace(dir, CurrentFile, data)
}

// AppendHistory appends line to dir/history.jsonl as one compact JSON line.
func AppendHistory(dir string, line *model.HistoryLine) error {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("output: %w", err)
	}
	data, err := json.Marshal(line)
	if err != nil {
		return fmt.Errorf("output: encode history: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, HistoryFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, filePerm)
	if err != nil {
		return fmt.Errorf("output: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("output: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("output: %w", err)
	}
	return nil
}

// UpdateDirectoryState merges this run's live endpoints into
// dir/directory-state.json at time now, drops every endpoint last seen more
// than a window ago, and rewrites the file atomically. A missing or unreadable
// previous state starts fresh: the file only feeds the "down for a while"
// label, so losing it costs nothing that matters. It returns the new state.
func UpdateDirectoryState(dir string, now time.Time, live []string, window time.Duration) (model.DirectoryState, error) {
	state := readDirectoryState(filepath.Join(dir, DirectoryStateFile))
	for _, ep := range live {
		state.Endpoints[ep] = now
	}
	for ep, seen := range state.Endpoints {
		if now.Sub(seen) > window {
			delete(state.Endpoints, ep)
		}
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return model.DirectoryState{}, fmt.Errorf("output: encode directory state: %w", err)
	}
	return state, replace(dir, DirectoryStateFile, data)
}

func readDirectoryState(path string) model.DirectoryState {
	state := model.DirectoryState{Endpoints: map[string]time.Time{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return state
	}
	var prev model.DirectoryState
	if err := json.Unmarshal(data, &prev); err != nil || prev.Endpoints == nil {
		return state
	}
	return prev
}

// replace writes data to dir/name through a temporary file and a rename.
func replace(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("output: %w", err)
	}
	tmp := filepath.Join(dir, name+".tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), filePerm); err != nil {
		return fmt.Errorf("output: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		return errors.Join(fmt.Errorf("output: %w", err), os.Remove(tmp))
	}
	return nil
}

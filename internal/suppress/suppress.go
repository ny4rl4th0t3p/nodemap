// Package suppress holds the opt-out set consulted at reduction time.
//
// The set stores salted SHA-256 hashes of node ids, never the ids. With the
// salt kept out of the repository, the published list reveals nothing about
// who opted out, even though node ids are enumerable by crawling. A node id
// enters this package only to be hashed and compared.
package suppress

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// hashHexLen is the length of a hex-encoded SHA-256.
const hashHexLen = sha256.Size * 2

var (
	// ErrNoSalt is returned when a non-empty list is loaded without a salt:
	// unsalted hashes of enumerable ids would expose the opt-out set.
	ErrNoSalt = errors.New("suppress: list is not empty but no salt is set")
	// ErrBadEntry is returned for a line that is not a hex SHA-256. A corrupt
	// list fails loudly rather than silently un-suppressing anyone.
	ErrBadEntry = errors.New("suppress: malformed entry")
)

// Set is the loaded opt-out set.
type Set struct {
	salt   string
	hashes map[string]struct{}
}

// Hash computes the salted hash of a node id. The id is trimmed and
// lower-cased first, since CometBFT prints ids as lower-case hex but
// operators may not.
func Hash(salt, nodeID string) string {
	h := sha256.New()
	h.Write([]byte(salt))
	h.Write([]byte{0})
	h.Write([]byte(strings.ToLower(strings.TrimSpace(nodeID))))
	return hex.EncodeToString(h.Sum(nil))
}

// Empty returns a set that suppresses nobody.
func Empty() *Set {
	return &Set{hashes: map[string]struct{}{}}
}

// Load reads one hex hash per line from path; blank lines and lines
// starting with '#' are ignored. An empty path yields an empty set. A
// missing file is an error: the operator asked for a list that is not there.
func Load(path, salt string) (*Set, error) {
	s := Empty()
	s.salt = salt
	if path == "" {
		return s, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("suppress: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.ToLower(line)
		if len(line) != hashHexLen {
			return nil, fmt.Errorf("%w: line %d", ErrBadEntry, n)
		}
		if _, err := hex.DecodeString(line); err != nil {
			return nil, fmt.Errorf("%w: line %d", ErrBadEntry, n)
		}
		s.hashes[line] = struct{}{}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("suppress: %w", err)
	}
	if len(s.hashes) > 0 && salt == "" {
		return nil, ErrNoSalt
	}
	return s, nil
}

// Len reports how many entries the set holds.
func (s *Set) Len() int { return len(s.hashes) }

// Suppressed reports whether nodeID has opted out. It is the function the
// crawler calls at reduction; the id is hashed and forgotten.
func (s *Set) Suppressed(nodeID string) bool {
	if len(s.hashes) == 0 {
		return false
	}
	_, ok := s.hashes[Hash(s.salt, nodeID)]
	return ok
}

package suppress

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	salt = "test-salt"
	idA  = "a000000000000000000000000000000000000000"
	idB  = "b000000000000000000000000000000000000001"
)

func writeList(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "suppression.txt")
	require.NoError(t, os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	return p
}

func TestHashIsSaltedAndNormalized(t *testing.T) {
	h := Hash(salt, idA)
	assert.Len(t, h, hashHexLen)
	assert.Equal(t, h, Hash(salt, strings.ToUpper(idA)), "case is normalized")
	assert.Equal(t, h, Hash(salt, "  "+idA+"\n"), "whitespace is normalized")
	assert.NotEqual(t, h, Hash("other-salt", idA), "salt has an effect")
	assert.NotEqual(t, h, Hash(salt, idB), "different ids differ")
	// The salt is length-delimited from the id so "ab"+"c" and "a"+"bc"
	// cannot be confused.
	assert.NotEqual(t, Hash("ab", "c"), Hash("a", "bc"))
}

func TestEmptyAndUnsetListSuppressNobody(t *testing.T) {
	assert.False(t, Empty().Suppressed(idA))
	s, err := Load("", "")
	require.NoError(t, err)
	assert.Equal(t, 0, s.Len())
	assert.False(t, s.Suppressed(idA))
}

func TestLoadAndMatch(t *testing.T) {
	p := writeList(t, "# opt-outs", "", strings.ToUpper(Hash(salt, idA)), "  "+Hash(salt, idB)+"  ")
	s, err := Load(p, salt)
	require.NoError(t, err)
	assert.Equal(t, 2, s.Len())
	for _, id := range []string{idA, strings.ToUpper(idA), idB} {
		assert.True(t, s.Suppressed(id), id)
	}
	assert.False(t, s.Suppressed("c000000000000000000000000000000000000002"))

	other, err := Load(p, "wrong-salt")
	require.NoError(t, err)
	assert.False(t, other.Suppressed(idA), "a different salt must not match")
}

func TestLoadRefusesUnsaltedNonEmptyList(t *testing.T) {
	_, err := Load(writeList(t, Hash(salt, idA)), "")
	assert.ErrorIs(t, err, ErrNoSalt)
	_, err = Load(writeList(t, "# nothing yet"), "")
	assert.NoError(t, err, "a comment-only list needs no salt")
}

func TestLoadRejectsMalformedEntries(t *testing.T) {
	cases := map[string]string{
		"not hex":   "not-a-hash",
		"bad chars": strings.Repeat("g", hashHexLen),
		"too short": Hash(salt, idA)[:hashHexLen-1],
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeList(t, Hash(salt, idB), bad), salt)
			assert.ErrorIs(t, err, ErrBadEntry)
		})
	}
}

func TestLoadMissingFileIsAnError(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing"), salt)
	assert.Error(t, err)
}

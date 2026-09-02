package crawl

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const delta = 1e-9

func TestMeshEmpty(t *testing.T) {
	s := newMesh().stats(10)
	assert.Equal(t, 0, s.Population)
	assert.Zero(t, s.LargestComponentFraction)
	assert.Zero(t, s.TopNShare)
}

func TestMeshIsolatedNodes(t *testing.T) {
	m := newMesh()
	m.touch("a")
	m.touch("b")
	m.touch("a") // idempotent
	s := m.stats(10)
	assert.Equal(t, 2, s.Population)
	assert.InDelta(t, 0.5, s.LargestComponentFraction, delta)
	assert.Zero(t, s.TopNShare, "no connections, no share")
}

func TestMeshTwoComponents(t *testing.T) {
	m := newMesh()
	// Component 1: star with hub h and leaves l1..l4 (5 nodes, 4 pairs).
	for _, l := range []string{"l1", "l2", "l3", "l4"} {
		m.connect("h", l)
	}
	// Component 2: a pair reported from both ends (2 nodes, 2 mentions each).
	m.connect("p", "q")
	m.connect("q", "p")
	m.connect("p", "p") // self-report is ignored
	s := m.stats(1)
	require.Equal(t, 7, s.Population)
	assert.InDelta(t, 5.0/7.0, s.LargestComponentFraction, delta)
	// Mentions: h=4, leaves 1 each, p=2, q=2: total 12; top-1 = h = 4.
	assert.InDelta(t, 4.0/12.0, s.TopNShare, delta)
	assert.InDelta(t, 1, m.stats(100).TopNShare, delta, "top-N beyond the population covers everything")
}

func TestMeshMergesComponentsInAnyOrder(t *testing.T) {
	m := newMesh()
	m.connect("a", "b")
	m.connect("c", "d")
	assert.InDelta(t, 0.5, m.stats(1).LargestComponentFraction, delta)
	m.connect("d", "a") // bridge the two pairs
	s := m.stats(1)
	assert.Equal(t, 4, s.Population)
	assert.InDelta(t, 1, s.LargestComponentFraction, delta)
	assert.Equal(t, m.find("b"), m.find("c"))
}

func TestMeshHoldsNoEdges(t *testing.T) {
	m := newMesh()
	m.connect("a", "b")
	m.connect("a", "c")
	// Everything the mesh remembers is per node: a parent, a size, a count.
	// There is no structure from which "a is connected to b" can be read.
	assert.Len(t, m.parent, 3)
	assert.Len(t, m.mentions, 3)
	assert.Equal(t, 2, m.mentions["a"])
	assert.Equal(t, 1, m.mentions["b"])
	assert.Equal(t, 1, m.mentions["c"])
	assert.Equal(t, 4, m.total, "two connections, two mentions each")
}

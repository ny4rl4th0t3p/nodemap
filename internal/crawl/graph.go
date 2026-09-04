package crawl

import "sort"

// mesh accumulates the two connectivity scalars without ever holding an
// edge. Each responder-to-peer pair is folded in as it arrives and then
// forgotten: union-find keeps one parent pointer and one size per node,
// which yields component sizes; a per-node count of connection mentions
// yields the hub share. Nothing here can be read back as who-connects-to-
// whom, in memory or anywhere else.
type mesh struct {
	parent   map[string]string
	size     map[string]int
	mentions map[string]int
	total    int // sum of mentions; two per pair folded in
}

func newMesh() *mesh {
	return &mesh{parent: map[string]string{}, size: map[string]int{}, mentions: map[string]int{}}
}

// touch ensures key is a node.
func (m *mesh) touch(key string) {
	if _, ok := m.parent[key]; !ok {
		m.parent[key] = key
		m.size[key] = 1
	}
}

// connect folds in one reported connection between a and b: both gain a
// mention and their components merge. The pair itself is not kept, so the
// same connection reported by both ends counts as two mentions, which is
// what "connections that terminate at" a node means.
func (m *mesh) connect(a, b string) {
	if a == b {
		return
	}
	m.touch(a)
	m.touch(b)
	m.mentions[a]++
	m.mentions[b]++
	m.total += 2
	ra, rb := m.find(a), m.find(b)
	if ra == rb {
		return
	}
	if m.size[ra] < m.size[rb] {
		ra, rb = rb, ra
	}
	m.parent[rb] = ra
	m.size[ra] += m.size[rb]
	delete(m.size, rb)
}

// find returns the root of key's component, halving the path as it goes.
func (m *mesh) find(key string) string {
	for m.parent[key] != key {
		m.parent[key] = m.parent[m.parent[key]]
		key = m.parent[key]
	}
	return key
}

// GraphStats are the two connectivity scalars the public map may carry.
// They are computed here so that no edge ever leaves this package, or
// exists in it.
type GraphStats struct {
	// Population is the number of nodes.
	Population int
	// LargestComponentFraction is the share of nodes in the largest
	// connected component; 1 means one mesh, lower means fragmentation.
	LargestComponentFraction float64
	// TopN is the N used for TopNShare.
	TopN int
	// TopNShare is the share of all connection mentions that terminate at
	// the N most-mentioned nodes.
	TopNShare float64
	// Mentions is the total number of connection mentions, the denominator
	// of every share derived from the mesh.
	Mentions int
}

// stats reduces the mesh to GraphStats.
func (m *mesh) stats(topN int) GraphStats {
	s := GraphStats{Population: len(m.parent), TopN: topN, Mentions: m.total}
	if s.Population == 0 {
		return s
	}
	largest := 0
	for _, n := range m.size { // size is kept for roots only
		largest = max(largest, n)
	}
	s.LargestComponentFraction = float64(largest) / float64(s.Population)
	if m.total == 0 {
		return s
	}
	counts := make([]int, 0, len(m.mentions))
	for _, n := range m.mentions {
		counts = append(counts, n)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(counts)))
	topN = min(topN, len(counts))
	top := 0
	for _, n := range counts[:topN] {
		top += n
	}
	s.TopNShare = float64(top) / float64(m.total)
	return s
}

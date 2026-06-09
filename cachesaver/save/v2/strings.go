package savev2

// dedupMap provides string deduplication during serialization.
// Each unique string is assigned a sequential uint32 ID.
// ID 0 is reserved for the empty string.
type dedupMap struct {
	m      map[string]uint32
	nextID uint32
}

func newDedupMap() *dedupMap {
	return &dedupMap{
		m:      map[string]uint32{"": 0},
		nextID: 1,
	}
}

// Add returns the ID for val, assigning a new one if not yet seen.
func (d *dedupMap) Add(val string) uint32 {
	if id, ok := d.m[val]; ok {
		return id
	}
	id := d.nextID
	d.m[val] = id
	d.nextID++
	return id
}

// Len returns the number of unique strings (including the empty string at id 0).
func (d *dedupMap) Len() int {
	return len(d.m)
}

// stringsDedup holds dedup maps for all five string categories.
// All categories share a single underlying map so strings from different
// categories get unique, non-overlapping IDs.
type stringsDedup struct {
	names        *dedupMap
	streets      *dedupMap
	houseNumbers *dedupMap
	cities       *dedupMap
	regions      *dedupMap
}

func newStringsDedup() *stringsDedup {
	shared := newDedupMap()
	return &stringsDedup{
		names:        shared,
		streets:      shared,
		houseNumbers: shared,
		cities:       shared,
		regions:      shared,
	}
}

// buildStringIndex produces the offset index and null-terminated string data block.
// Strings are ordered by ID (0 = empty, 1 = first registered, etc.).
func buildStringIndex(dedup *stringsDedup) (offsetIndex []uint32, dataBlock []byte) {
	dm := dedup.names // shared across all categories
	n := dm.Len()

	// Build reverse mapping: id → string
	byID := make([]string, n)
	for s, id := range dm.m {
		byID[id] = s
	}

	// Compute offsets
	offsetIndex = make([]uint32, n)
	pos := uint32(0)
	for id := 0; id < n; id++ {
		offsetIndex[id] = pos
		pos += uint32(len(byID[id])) + 1 // +1 for null terminator
	}

	// Build data block
	dataBlock = make([]byte, pos)
	for id := 0; id < n; id++ {
		s := byID[id]
		copy(dataBlock[offsetIndex[id]:], s)
		dataBlock[offsetIndex[id]+uint32(len(s))] = 0 // null terminator
	}

	return offsetIndex, dataBlock
}

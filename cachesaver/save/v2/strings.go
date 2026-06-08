package savev2

// dedupMap provides string deduplication during serialization.
// Each unique string is assigned a byte offset and length in the output string blob.
// The empty string is always at offset 0 with length 0.
type dedupMap struct {
	m      map[string]strOff
	nextOff int64
}

func newDedupMap() *dedupMap {
	return &dedupMap{
		m:      map[string]strOff{"": {offset: 0, length: 0}},
		nextOff: 0,
	}
}

// Add returns the (offset, length) for val, assigning a new one if not yet seen.
func (d *dedupMap) Add(val string) strOff {
	if off, ok := d.m[val]; ok {
		return off
	}
	off := strOff{offset: d.nextOff, length: uint32(len(val))}
	d.m[val] = off
	d.nextOff += int64(len(val))
	return off
}

// Blob returns the concatenated string blob.
func (d *dedupMap) Blob() []byte {
	// Build blob: strings concatenated in order of first addition.
	// We need to reconstruct the order. Since Go map iteration is random,
	// we iterate by offset instead.
	blob := make([]byte, d.nextOff)
	for s, off := range d.m {
		if off.length == 0 {
			continue
		}
		copy(blob[off.offset:off.offset+int64(off.length)], s)
	}
	return blob
}

// stringsDedup holds dedup maps for all five string categories.
// All categories share a single offset space so strings don't overlap in the blob.
type stringsDedup struct {
	names        *dedupMap
	streets      *dedupMap
	houseNumbers *dedupMap
	cities       *dedupMap
	regions      *dedupMap
}

func newStringsDedup() *stringsDedup {
	// All maps share the same underlying map and offset counter so strings
	// from different categories are stored sequentially without overlap.
	shared := newDedupMap()
	return &stringsDedup{
		names:        shared,
		streets:      shared,
		houseNumbers: shared,
		cities:       shared,
		regions:      shared,
	}
}

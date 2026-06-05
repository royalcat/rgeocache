package savev2

// uniqueMap provides string deduplication during serialization.
// Index 0 is always the empty string.
type uniqueMap struct {
	m map[string]int
	i int
}

func newUniqueMap() *uniqueMap {
	return &uniqueMap{
		m: map[string]int{"": 0},
		i: 0,
	}
}

// Add returns the index for val, assigning a new one if not yet seen.
func (u *uniqueMap) Add(val string) int {
	if idx, ok := u.m[val]; ok {
		return idx
	}
	u.i++
	u.m[val] = u.i
	return u.i
}

// Slice returns a slice where each unique string is at its assigned index.
// Index 0 is always the empty string.
func (u *uniqueMap) Slice() []string {
	s := make([]string, u.i+1)
	for v, idx := range u.m {
		s[idx] = v
	}
	return s
}

// stringsDedup holds dedup maps for all five string categories.
type stringsDedup struct {
	names        *uniqueMap
	streets      *uniqueMap
	houseNumbers *uniqueMap
	cities       *uniqueMap
	regions      *uniqueMap
}

func newStringsDedup() *stringsDedup {
	return &stringsDedup{
		names:        newUniqueMap(),
		streets:      newUniqueMap(),
		houseNumbers: newUniqueMap(),
		cities:       newUniqueMap(),
		regions:      newUniqueMap(),
	}
}

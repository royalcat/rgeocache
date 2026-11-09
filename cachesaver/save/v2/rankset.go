package savev2

import "math/bits"

// rankSet is a bitset over [0, n) that answers "how many set bits precede i"
// in O(1). The writer uses it as a dense-id map: setting the bit of every
// canonical road occurrence makes Rank(occ) its dense graph node id, at about
// n/8 + n/128 bytes instead of the 4n bytes of an explicit uint32 table.
type rankSet struct {
	words []uint64
	// blocks[i] is the number of set bits in words[0 : i*rankBlockWords].
	blocks []uint32
	total  uint32
}

const rankBlockWords = 8

func newRankSet(n int) *rankSet {
	w := (n + 63) / 64
	return &rankSet{
		words:  make([]uint64, w),
		blocks: make([]uint32, (w+rankBlockWords-1)/rankBlockWords+1),
	}
}

func (r *rankSet) Set(i uint32) { r.words[i>>6] |= 1 << (i & 63) }

func (r *rankSet) Has(i uint32) bool { return r.words[i>>6]&(1<<(i&63)) != 0 }

// Finish builds the rank blocks. Set must not be called afterwards.
func (r *rankSet) Finish() {
	var acc uint32
	for i, w := range r.words {
		if i%rankBlockWords == 0 {
			r.blocks[i/rankBlockWords] = acc
		}
		acc += uint32(bits.OnesCount64(w))
	}
	r.blocks[(len(r.words)+rankBlockWords-1)/rankBlockWords] = acc
	r.total = acc
}

// Count returns the number of set bits.
func (r *rankSet) Count() int { return int(r.total) }

// Rank returns the number of set bits strictly below i.
func (r *rankSet) Rank(i uint32) uint32 {
	w := int(i >> 6)
	n := r.blocks[w/rankBlockWords]
	for j := w &^ (rankBlockWords - 1); j < w; j++ {
		n += uint32(bits.OnesCount64(r.words[j]))
	}
	return n + uint32(bits.OnesCount64(r.words[w]&(1<<(i&63)-1)))
}

// invertPermutation replaces perm, where perm[pos] = orig, by its inverse
// (perm[orig] = pos) without a second array. It needs only n/8 bytes of
// scratch.
func invertPermutation(perm []uint32) {
	visited := make([]uint64, (len(perm)+63)/64)
	for s := range perm {
		if visited[s>>6]&(1<<(s&63)) != 0 {
			continue
		}
		// Walk the cycle s -> c1 -> c2 -> ... -> ck -> s and point every
		// element back at its predecessor.
		visited[s>>6] |= 1 << (s & 63)
		prev := uint32(s)
		cur := perm[s]
		for int(cur) != s {
			next := perm[cur]
			perm[cur] = prev
			visited[cur>>6] |= 1 << (cur & 63)
			prev, cur = cur, next
		}
		perm[s] = prev
	}
}

package savev2

import (
	"math/rand"
	"testing"
)

func TestRankSet(t *testing.T) {
	for _, n := range []int{1, 63, 64, 65, 511, 512, 513, 5000} {
		rnd := rand.New(rand.NewSource(int64(n)))
		r := newRankSet(n)
		want := make([]uint32, n+1)
		set := make([]bool, n)
		for i := 0; i < n; i++ {
			set[i] = rnd.Intn(3) == 0
			if set[i] {
				r.Set(uint32(i))
			}
		}
		r.Finish()
		var acc uint32
		for i := 0; i < n; i++ {
			want[i] = acc
			if set[i] {
				acc++
			}
		}
		if r.Count() != int(acc) {
			t.Fatalf("n=%d count %d want %d", n, r.Count(), acc)
		}
		for i := 0; i < n; i++ {
			if r.Has(uint32(i)) != set[i] {
				t.Fatalf("n=%d Has(%d)", n, i)
			}
			if got := r.Rank(uint32(i)); got != want[i] {
				t.Fatalf("n=%d Rank(%d)=%d want %d", n, i, got, want[i])
			}
		}
	}
}

func TestInvertPermutation(t *testing.T) {
	for _, n := range []int{0, 1, 2, 7, 1000} {
		rnd := rand.New(rand.NewSource(int64(n) + 9))
		perm := make([]uint32, n)
		for i, v := range rnd.Perm(n) {
			perm[i] = uint32(v)
		}
		orig := append([]uint32(nil), perm...)
		invertPermutation(perm)
		for pos, o := range orig {
			if perm[o] != uint32(pos) {
				t.Fatalf("n=%d inv[%d]=%d want %d", n, o, perm[o], pos)
			}
		}
	}
}

func TestWriterPeakBytesScales(t *testing.T) {
	small, large := writerPeakBytes(10_000_000, 9_000_000), writerPeakBytes(100_000_000, 90_000_000)
	if large <= small*5 {
		t.Fatalf("peak does not scale with input: %d vs %d", small, large)
	}
}

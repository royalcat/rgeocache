package geocoder

// rankPoint orders candidates for Find. The score is the squared distance
// divided by the squared weight — ranking by distance ÷ weight, so a heavier
// kind wins at a proportionally larger distance — and exact score ties fall
// back to longitude then latitude. The rank is a function of the candidate
// alone, so the winner is independent of the order the KD-tree traversal
// happens to visit points in.
type rankPoint struct {
	score float64
	x, y  float64
}

// less reports whether r is a better candidate than o.
func (r rankPoint) less(o rankPoint) bool {
	if r.score != o.score {
		return r.score < o.score
	}
	if r.x != o.x {
		return r.x < o.x
	}
	return r.y < o.y
}

// pointRank scores one candidate. A weight of 0 (which the generator does not
// produce) is treated as the weakest class, so an unknown-weight point is
// still found instead of being dropped from every lookup.
func pointRank(lon, lat, x, y float64, weight uint8) rankPoint {
	w := float64(weight)
	if w < 1 {
		w = 1
	}
	return rankPoint{
		score: distanceSquared(lon, lat, x, y) / (w * w),
		x:     x,
		y:     y,
	}
}

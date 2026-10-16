package geocoder

// buildingPriorityRadiusDegrees is the radius within which a building always
// outranks a non-building candidate, whatever the distance ÷ weight score
// says. CRUTCH: it is a coarse degree-space radius (about 100 m north-south,
// narrowing with the latitude) added so reverse geocoding prefers the
// building at an address point over the street the point sits on; replace it
// with a proper relevance model when one exists. See AGENTS.md.
const buildingPriorityRadiusDegrees = 100.0 / 111_320.0

// buildingMinWeight is the weight at and above which a point counts as a
// building for the priority tier. The generator gives buildings weight 10 and
// every other kind less.
const buildingMinWeight = 10

// rankPoint orders candidates for Find. The primary key is the tier: a
// building (weight >= buildingMinWeight) within buildingPriorityRadiusDegrees
// of the query is tier 0 and wins over every tier-1 candidate. Within a tier
// the score orders: the squared distance divided by the squared weight —
// ranking by distance ÷ weight, so a heavier kind wins at a proportionally
// larger distance — and exact score ties fall back to longitude then
// latitude. The rank is a function of the candidate alone, so the winner is
// independent of the order the KD-tree traversal happens to visit points in.
type rankPoint struct {
	tier  uint8
	score float64
	x, y  float64
}

// less reports whether r is a better candidate than o.
func (r rankPoint) less(o rankPoint) bool {
	if r.tier != o.tier {
		return r.tier < o.tier
	}
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
	dist2 := distanceSquared(lon, lat, x, y)
	tier := uint8(1)
	if weight >= buildingMinWeight && dist2 <= buildingPriorityRadiusDegrees*buildingPriorityRadiusDegrees {
		tier = 0
	}
	return rankPoint{
		tier:  tier,
		score: dist2 / (w * w),
		x:     x,
		y:     y,
	}
}

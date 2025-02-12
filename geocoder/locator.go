package geocoder

import (
	"math"

	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/kdbush"
)

type RGeoCoder struct {
	tree *kdbush.KDBush[geomodel.Info]
}

const maxRadius float64 = 0.01
const thresholdRadius float64 = 1e-7

type InfoModel struct {
	geomodel.Info
}

func (f *RGeoCoder) Find(lat, lon float64) (i InfoModel, ok bool) {
	finPoint := kdbush.Point[geomodel.Info]{}
	finDist := math.Inf(1)
	f.tree.Within(lon, lat, maxRadius, func(p kdbush.Point[geomodel.Info]) bool {
		dist := distanceSquared(lon, lat, p.X, p.Y)
		if dist < finDist {
			finPoint = p
			finDist = dist
		}

		// if dist < thresholdRadius {
		// 	return false
		// }

		return true
	})

	if math.IsInf(finDist, 1) {
		return InfoModel{}, false
	}

	return InfoModel{Info: finPoint.Data}, true
}

func distanceSquared(x1, y1, x2, y2 float64) (distance float64) {
	d0 := (x1 - x2)
	d1 := (y1 - y2)
	return d0*d0 + d1*d1
}

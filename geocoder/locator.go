package geocoder

import (
	"math"
	"rgeocache/geomodel"
	"rgeocache/kdbush"
)

type RGeoCoder struct {
	tree *kdbush.KDBush[geomodel.Info]
}

const defaultRadius float64 = 0.01
const thesholdDist float64 = 1e-5

type InfoModel struct {
	geomodel.Info
}

func (f *RGeoCoder) Find(lat, lon float64) (i InfoModel, ok bool) {
	finPoint := kdbush.Point[geomodel.Info]{}
	finDist := math.Inf(1)
	f.tree.Within(lat, lon, defaultRadius, func(p kdbush.Point[geomodel.Info]) bool {
		dist := distanceSquared(lat, lon, p.X, p.Y)
		if dist < finDist {
			finPoint = p
			finDist = dist
		}

		if dist < thesholdDist {
			return false
		}

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

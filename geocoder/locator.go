package geocoder

import (
	"log/slog"
	"math"
	"unique"

	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/kdbush"
)

// internal memory-optimized representation of geomodel.Info
type geoInfo struct {
	Name        string
	Street      unique.Handle[string]
	HouseNumber unique.Handle[string]
	City        unique.Handle[string]
	Region      unique.Handle[string]
	Weight      uint8
}

func (g *geoInfo) value() geomodel.Info {
	return geomodel.Info{
		Name:        g.Name,
		Street:      g.Street.Value(),
		HouseNumber: g.HouseNumber.Value(),
		City:        g.City.Value(),
		Region:      g.Region.Value(),
	}
}

type RGeoCoder struct {
	tree *kdbush.KDBush[*geoInfo]

	searchRadius float64
	logger       *slog.Logger
}

type InfoModel struct {
	geomodel.Info
}

func (f *RGeoCoder) Find(lat, lon float64) (i InfoModel, ok bool) {
	return f.FindInRadius(lat, lon, f.searchRadius)
}

func (f *RGeoCoder) FindInRadius(lat, lon float64, radius float64) (i InfoModel, ok bool) {
	finPoint := kdbush.Point[*geoInfo]{}
	finDist := math.Inf(1)
	f.tree.Within(lon, lat, radius, func(p kdbush.Point[*geoInfo]) bool {
		dist := distanceSquared(lon, lat, p.X, p.Y)
		if dist < finDist || p.Data.Weight > finPoint.Data.Weight {
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

	return InfoModel{Info: finPoint.Data.value()}, true
}

func distanceSquared(x1, y1, x2, y2 float64) (distance float64) {
	d0 := (x1 - x2)
	d1 := (y1 - y2)
	return d0*d0 + d1*d1
}

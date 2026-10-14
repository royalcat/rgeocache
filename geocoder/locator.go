package geocoder

import (
	"log/slog"
	"math"
	"unique"

	"github.com/paulmach/orb"
	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/internal/bordertree"
	"github.com/royalcat/rgeocache/kdbush"
)

// internal memory-optimized representation of geomodel.Info
type geoInfo struct {
	Name        unique.Handle[string]
	Street      unique.Handle[string]
	HouseNumber unique.Handle[string]
	City        unique.Handle[string]
	Region      unique.Handle[string]
	Weight      uint8
}

func (g *geoInfo) value() geomodel.Info {
	return geomodel.Info{
		Name:        g.Name.Value(),
		Street:      g.Street.Value(),
		HouseNumber: g.HouseNumber.Value(),
		City:        g.City.Value(),
		Region:      g.Region.Value(),
		Weight:      g.Weight,
	}
}

// Geocoder is the common interface for both in-memory and disk-backed geocoders.
type Geocoder interface {
	Find(lat, lon float64) (InfoModel, bool)
	// PointsInRadius returns up to limit cached points within radius degrees of
	// (lat, lon), nearest first, and the total number of points that matched
	// (which exceeds len(points) when the result was truncated). The kind
	// filter is a bitmask; a zero mask matches nothing.
	PointsInRadius(lat, lon, radius float64, limit int, kinds PointKind) ([]PointInRadius, int)
}

type RGeoCoder struct {
	tree         *kdbush.KDBush[*geoInfo]
	regions      *bordertree.BorderTree[unique.Handle[string]]
	countries    *bordertree.BorderTree[unique.Handle[string]]
	zones        []cachemodel.Zone
	metadata     cachemodel.Metadata
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
	finRank := rankPoint{score: math.Inf(1)}
	hasBest := false

	f.tree.Within(lon, lat, radius, func(p kdbush.Point[*geoInfo]) bool {
		rank := pointRank(lon, lat, p.X, p.Y, p.Data.Weight)
		if !hasBest || rank.less(finRank) {
			finPoint = p
			finRank = rank
			hasBest = true
		}

		// if dist < thresholdRadius {
		// 	return false
		// }

		return true
	})

	// point found (happy path)
	if hasBest {
		out := InfoModel{Info: finPoint.Data.value()}

		if out.Region == "" && f.regions != nil {
			if region, ok := f.regions.QueryPoint(orb.Point{lon, lat}); ok {
				out.Region = region.Value()
			}
		}

		if out.Country == "" && f.countries != nil {
			if country, ok := f.countries.QueryPoint(orb.Point{lon, lat}); ok {
				out.Country = country.Value()
			}
		}

		return out, true
	}

	// point not found, trying determine region and country by borders
	out := InfoModel{}
	if f.countries != nil {
		country, ok := f.countries.QueryPoint(orb.Point{lon, lat})
		if ok {
			out.Country = country.Value()
		}
	}
	if f.regions != nil {
		region, ok := f.regions.QueryPoint(orb.Point{lon, lat})
		if ok {
			out.Region = region.Value()
		}
	}
	if out.Country != "" || out.Region != "" {
		return out, true
	}

	// nothing found
	return InfoModel{}, false
}

// PointsInRadius returns up to limit cached points within radius degrees of
// (lat, lon), nearest first, together with the total number of matching points.
func (f *RGeoCoder) PointsInRadius(lat, lon, radius float64, limit int, kinds PointKind) ([]PointInRadius, int) {
	collector := newRadiusCollector[*geoInfo](limit)

	f.tree.Within(lon, lat, radius, func(p kdbush.Point[*geoInfo]) bool {
		kind := PointKindFromWeight(p.Data.Weight)
		if kinds&kind == 0 {
			return true
		}
		collector.add(p.X, p.Y, distanceSquared(lon, lat, p.X, p.Y), p.Data)
		return true
	})

	hits := collector.sorted()
	points := make([]PointInRadius, 0, len(hits))
	for _, hit := range hits {
		info := hit.data.value()
		fillRegionCountry(&info, f.regions, f.countries, hit.x, hit.y)
		points = append(points, PointInRadius{
			X:    hit.x,
			Y:    hit.y,
			Info: info,
			Kind: PointKindFromWeight(hit.data.Weight),
		})
	}
	return points, collector.total
}

// Zones returns the cache's region and country zones.
func (f *RGeoCoder) Zones() []cachemodel.Zone {
	return f.zones
}

// Metadata returns the cache metadata. The zero value is returned for legacy
// caches that carry none.
func (f *RGeoCoder) Metadata() cachemodel.Metadata {
	return f.metadata
}

// NumPoints returns the number of points in the cache.
func (f *RGeoCoder) NumPoints() int {
	return f.tree.Len()
}

// ForEachPoint calls fn for every cached point. Iteration stops early when fn
// returns false.
func (f *RGeoCoder) ForEachPoint(fn func(cachemodel.Point) bool) {
	f.tree.ForEach(func(p kdbush.Point[*geoInfo]) bool {
		return fn(cachemodel.Point{
			X: p.X,
			Y: p.Y,
			Data: cachemodel.Info{
				Name:        p.Data.Name,
				Street:      p.Data.Street,
				HouseNumber: p.Data.HouseNumber,
				City:        p.Data.City,
				Region:      p.Data.Region,
				Weight:      p.Data.Weight,
			},
		})
	})
}

// CountryAt returns the name of the country containing the given coordinates.
func (f *RGeoCoder) CountryAt(lat, lon float64) (string, bool) {
	if f.countries == nil {
		return "", false
	}
	country, ok := f.countries.QueryPoint(orb.Point{lon, lat})
	if !ok {
		return "", false
	}
	return country.Value(), true
}

func distanceSquared(x1, y1, x2, y2 float64) (distance float64) {
	d0 := (x1 - x2)
	d1 := (y1 - y2)
	return d0*d0 + d1*d1
}

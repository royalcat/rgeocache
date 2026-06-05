package geocoder

import (
	"log/slog"
	"math"
	"unique"

	"github.com/paulmach/orb"
	"github.com/royalcat/rgeocache/cachesaver/save/v2"
	"github.com/royalcat/rgeocache/internal/bordertree"
	"github.com/royalcat/rgeocache/kdbush"
	"golang.org/x/exp/mmap"
)

// RGeoCoderDisk is a disk-backed reverse geocoder that uses mmap for the
// spatial index. Point data is read lazily from disk only for spatial matches.
//
// The zero value is not usable; use LoadGeoCoderFromFileDisk to create one.
type RGeoCoderDisk struct {
	diskTree        *kdbush.DiskKDBush[savev2.V2PointData, *savev2.V2PointData]
	nameHandles     []unique.Handle[string]
	streetHandles   []unique.Handle[string]
	houseNumHandles []unique.Handle[string]
	cityHandles     []unique.Handle[string]
	regionHandles   []unique.Handle[string]
	regions         *bordertree.BorderTree[unique.Handle[string]]
	countries       *bordertree.BorderTree[unique.Handle[string]]
	searchRadius    float64
	logger          *slog.Logger
	mmapReader      *mmap.ReaderAt // kept for Close()
}

// Find returns the closest address for the given coordinates.
func (f *RGeoCoderDisk) Find(lat, lon float64) (InfoModel, bool) {
	return f.FindInRadius(lat, lon, f.searchRadius)
}

// FindInRadius returns the closest address within the given radius.
func (f *RGeoCoderDisk) FindInRadius(lat, lon float64, radius float64) (i InfoModel, ok bool) {
	finPoint := kdbush.Point[savev2.V2PointData]{}
	finDist := math.Inf(1)
	hasBest := false

	err := f.diskTree.Within(lon, lat, radius, func(p kdbush.Point[savev2.V2PointData]) bool {
		dist := distanceSquared(lon, lat, p.X, p.Y)
		if dist < finDist || p.Data.Weight > finPoint.Data.Weight {
			finPoint = p
			finDist = dist
			hasBest = true
		}
		return true
	})
	if err != nil {
		f.logger.Error("error querying disk tree", "error", err)
		return InfoModel{}, false
	}

	// Point found (happy path)
	if hasBest {
		gi := f.resolvePointData(finPoint.Data)
		out := InfoModel{Info: gi.value()}

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

	// Point not found, try region/country from borders alone
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

	return InfoModel{}, false
}

// resolvePointData converts V2PointData indices to a geoInfo using pre-resolved
// handle arrays. O(1) array lookups — no map access at query time.
func (f *RGeoCoderDisk) resolvePointData(data savev2.V2PointData) *geoInfo {
	return &geoInfo{
		Name:        resolveHandle(f.nameHandles, data.NameStrIdx),
		Street:      resolveHandle(f.streetHandles, data.StreetStrIdx),
		HouseNumber: resolveHandle(f.houseNumHandles, data.HouseNumberStrIdx),
		City:        resolveHandle(f.cityHandles, data.CityStrIdx),
		Region:      resolveHandle(f.regionHandles, data.RegionStrIdx),
		Weight:      data.Weight,
	}
}

// resolveHandle safely looks up a handle by index.
// Returns the zero value (empty string) if the index is out of bounds.
func resolveHandle(handles []unique.Handle[string], idx uint32) unique.Handle[string] {
	if int(idx) < len(handles) {
		return handles[idx]
	}
	return unique.Make("")
}

// Close releases the mmap resources. The geocoder must not be used after Close.
func (f *RGeoCoderDisk) Close() error {
	if f.mmapReader != nil {
		return f.mmapReader.Close()
	}
	return nil
}

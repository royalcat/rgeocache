package geocoder

import (
	"log/slog"
	"math"
	"unique"

	"github.com/paulmach/orb"
	savev2 "github.com/royalcat/rgeocache/cachesaver/save/v2"
	"github.com/royalcat/rgeocache/internal/bordertree"
	"github.com/royalcat/rgeocache/kdbush"
	"golang.org/x/exp/mmap"
)

// RGeoCoderDisk is a disk-backed reverse geocoder that uses mmap for the
// spatial index and for lazy string resolution. Strings are read from the
// mmap'd file only when a point is matched.
type RGeoCoderDisk struct {
	diskTree          *kdbush.DiskKDBush[savev2.V2PointData, *savev2.V2PointData]
	mmapReader        *mmap.ReaderAt
	stringsIndex      []uint32 // offset index: id → byte offset into string data
	stringsDataOffset int64    // byte offset of the string data block in the mmap'd file
	regions           *bordertree.BorderTree[unique.Handle[string]]
	countries         *bordertree.BorderTree[unique.Handle[string]]
	searchRadius      float64
	logger            *slog.Logger
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

	// Fallback: region/country from borders alone
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

// resolvePointData reads strings lazily from the mmap'd string data block.
func (f *RGeoCoderDisk) resolvePointData(data savev2.V2PointData) *geoInfo {
	return &geoInfo{
		Name:        f.readStr(data.NameID),
		Street:      f.readStr(data.StreetID),
		HouseNumber: f.readStr(data.HouseNumberID),
		City:        f.readStr(data.CityID),
		Region:      f.readStr(data.RegionID),
		Weight:      data.Weight,
	}
}

// readStr reads a null-terminated string from the mmap'd string data block by ID.
func (f *RGeoCoderDisk) readStr(id uint32) unique.Handle[string] {
	if id == 0 {
		return unique.Make("")
	}
	start := f.stringsIndex[id]
	// Read a buffer large enough for any address string.
	// The null terminator tells us where the string ends.
	buf := make([]byte, 512)
	n, err := f.mmapReader.ReadAt(buf, f.stringsDataOffset+int64(start))
	if err != nil && n == 0 {
		return unique.Make("")
	}
	// Find null terminator
	end := n
	for i := 0; i < n; i++ {
		if buf[i] == 0 {
			end = i
			break
		}
	}
	return unique.Make(string(buf[:end]))
}

// Close releases the mmap resources.
func (f *RGeoCoderDisk) Close() error {
	if f.mmapReader != nil {
		return f.mmapReader.Close()
	}
	return nil
}

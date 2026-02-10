package cachesaver

import (
	"encoding/binary"
	"io"
	"time"

	savev1 "github.com/royalcat/rgeocache/cachesaver/save/v1"
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/kdbush"
)

type Metadata struct {
	Version     uint32
	Locale      string
	DateCreated time.Time
}

type Zone struct {
	Name    string
	Bounds  [4]float64     // [minX, minY, maxX, maxY]
	Polygon [][][2]float64 // orb multipolygon
}

func Save(points []kdbush.Point[geomodel.Info], meta Metadata, w io.Writer) error {
	_, err := w.Write(MAGIC_BYTES)
	if err != nil {
		return err
	}

	err = binary.Write(w, binary.LittleEndian, savev1.COMPATIBILITY_LEVEL)
	if err != nil {
		return err
	}

	cache := savev1.CacheFromPoints(points)
	cache.DateCreated = meta.DateCreated.Format(time.RFC3339)
	cache.Locale = meta.Locale
	cache.Version = meta.Version

	err = savev1.Save(w, cache)
	if err != nil {
		return err
	}
	return nil
}

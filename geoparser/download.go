package geoparser

import (
	"context"
	"encoding/gob"
	"os"

	"github.com/royalcat/btrgo"
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/kdbush"
)

// TODO
// https://download.geofabrik.de/russia-latest.osm.pbf
func DownloadOsm(ctx context.Context, name string) {

}

func (f *GeoGen) SavePointsToFile(file string) error {
	dataFile, err := os.Create(file)

	if err != nil {
		return err
	}
	// serialize the data
	dataEncoder := gob.NewEncoder(dataFile)

	f.parsedPointsMu.Lock()
	defer f.parsedPointsMu.Unlock()

	// TODO optimize
	f.parsedPoints = btrgo.SliceUnique(f.parsedPoints)

	points := make([]kdbush.Point[geomodel.Info], 0, len(f.parsedPoints)) // TODO preallocate
	for _, point := range f.parsedPoints {
		points = append(points, kdbush.Point[geomodel.Info]{
			X:    point.Lat(),
			Y:    point.Lon(),
			Data: point.Info,
		})
	}

	err = dataEncoder.Encode(points)
	if err != nil {
		return err
	}
	return dataFile.Close()
}

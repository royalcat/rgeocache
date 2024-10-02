package geoparser

import (
	"context"
	"encoding/gob"
	"os"

	"github.com/paulmach/orb"
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

	points := make([]kdbush.Point[geomodel.Info], 0) // TODO preallocate
	f.points.Range(func(point orb.Point, info geomodel.Info) bool {
		points = append(points, kdbush.Point[geomodel.Info]{
			X:    point[0],
			Y:    point[1],
			Data: info,
		})
		return true
	})

	err = dataEncoder.Encode(points)
	if err != nil {
		return err
	}
	return dataFile.Close()
}

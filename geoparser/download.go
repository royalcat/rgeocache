package geoparser

import (
	"context"
	"encoding/gob"
	"os"
)

// TODO
// https://download.geofabrik.de/russia-latest.osm.pbf
func DownloadOsm(ctx context.Context, name string) {

}

func (f *GeoGen) SavePointsToFile(file string) error {
	f.pointsMutex.Lock()
	defer f.pointsMutex.Unlock()

	dataFile, err := os.Create(file)

	if err != nil {
		return err
	}
	// serialize the data
	dataEncoder := gob.NewEncoder(dataFile)
	err = dataEncoder.Encode(f.points)
	if err != nil {
		return err
	}
	return dataFile.Close()
}

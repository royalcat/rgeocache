package geoparser

import (
	"context"
	"encoding/gob"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

// https://download.geofabrik.de/russia-latest.osm.pbf
func DownloadOsm(ctx context.Context, name string) {

}

func (f *GeoGen) SavePointsToFile(file string) error {
	f.pointsMutex.Lock()
	defer f.pointsMutex.Unlock()

	dataFile, err := os.Create(file + ".gob.zstd")

	if err != nil {
		return err
	}

	var writer io.Writer

	writer, err = zstd.NewWriter(dataFile)
	if err != nil {
		return err
	}

	// serialize the data
	dataEncoder := gob.NewEncoder(writer)
	dataEncoder.Encode(f.points)

	dataFile.Close()
	return nil
}

package geocoder

import (
	"encoding/gob"
	"fmt"
	"os"

	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/kdbush"
)

func (f *RGeoCoder) LoadFromPointsFile(file string) error {
	dataFile, err := os.Open(file)
	if err != nil {
		return fmt.Errorf("can`t open file error: %s", err.Error())
	}
	defer dataFile.Close()

	var points []kdbush.Point[geomodel.Info]
	dataEncoder := gob.NewDecoder(dataFile)
	err = dataEncoder.Decode(&points)
	if err != nil {
		return fmt.Errorf("error decoding points file: %s", err.Error())
	}

	f.tree = kdbush.NewBush(points, 256)
	return nil
}

package geocoder

import (
	"encoding/gob"
	"io"
	"os"
	"rgeocache/geomodel"
	"rgeocache/kdbush"
	"strings"

	"github.com/klauspost/compress/zstd"
)

func (f *RGeoCoder) LoadFromPointsFile(file string) error {
	dataFile, err := os.Open(file)
	if err != nil {
		return err
	}
	defer dataFile.Close()
	var reader io.Reader
	if strings.HasSuffix(file, ".zstd") {
		reader, err = zstd.NewReader(dataFile)
	} else {
		reader = dataFile
	}

	if err != nil {
		return err
	}

	var points []kdbush.Point[geomodel.Info]
	dataEncoder := gob.NewDecoder(reader)
	err = dataEncoder.Decode(&points)
	if err != nil {
		return err
	}

	f.tree = kdbush.NewBush(points, 256)
	return nil
}

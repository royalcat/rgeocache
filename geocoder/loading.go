package geocoder

import (
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/kdbush"
)

func NewRGeoCoder() *RGeoCoder {
	return &RGeoCoder{
		tree: kdbush.NewBush[geomodel.Info](nil, 256),
	}
}

func (f *RGeoCoder) LoadFromPointsFile(file string) error {
	dataFile, err := os.Open(file)
	if err != nil {
		return fmt.Errorf("can`t open file error: %s", err.Error())
	}
	defer dataFile.Close()

	var reader io.Reader
	if strings.HasSuffix(file, ".zst") {
		dec, err := zstd.NewReader(dataFile, zstd.WithDecoderConcurrency(0))
		if err != nil {
			return err
		}
		defer dec.Close()
		reader = dec
	} else {
		reader = dataFile
	}

	var points []kdbush.Point[geomodel.Info]
	dataEncoder := gob.NewDecoder(reader)
	err = dataEncoder.Decode(&points)
	if err != nil {
		return fmt.Errorf("error decoding points file: %s", err.Error())
	}

	f.tree = kdbush.NewBush(points, 256)
	return nil
}

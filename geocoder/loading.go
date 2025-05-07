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

func (f *RGeoCoder) LoadFromPointsFile(file string) error {
	reader, err := openReader(file)
	if err != nil {
		return fmt.Errorf("error opening points file: %s", err.Error())
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

func openReader(name string) (io.ReadCloser, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, fmt.Errorf("can`t open file error: %s", err.Error())
	}

	if strings.HasSuffix(name, ".zst") {
		dec, err := zstd.NewReader(file, zstd.WithDecoderConcurrency(0))
		if err != nil {
			return nil, fmt.Errorf("can`t create zstd reader: %s", err.Error())
		}
		return dec.IOReadCloser(), nil
	}

	return file, nil
}

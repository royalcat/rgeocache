package cachesaver

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"io"

	savev1 "github.com/royalcat/rgeocache/cachesaver/save/v1"
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/kdbush"
)

func LoadFromReader(reader io.Reader) ([]kdbush.Point[geomodel.Info], error) {
	magic := make([]byte, len(MAGIC_BYTES))
	_, err := reader.Read(magic)
	if err != nil {
		return nil, fmt.Errorf("error reading magic bytes: %s", err.Error())
	}

	// If the magic bytes are not equal to the expected value, we assume it's a legacy format
	if string(magic) != string(MAGIC_BYTES) {
		return legacyLoader(io.MultiReader(bytes.NewReader(magic), reader))
	}

	var compatibilityLevel uint32
	err = binary.Read(reader, binary.LittleEndian, &compatibilityLevel)
	if err != nil {
		return nil, fmt.Errorf("error reading compatibility level: %s", err.Error())
	}

	switch compatibilityLevel {
	case savev1.COMPATIBILITY_LEVEL:
		return loadV1Cache(reader)
	}

	return nil, fmt.Errorf("unsupported compatibility level: %d", compatibilityLevel)

}

func legacyLoader(reader io.Reader) ([]kdbush.Point[geomodel.Info], error) {
	decoder := gob.NewDecoder(reader)
	var points []kdbush.Point[geomodel.Info]
	err := decoder.Decode(&points)
	if err != nil {
		return nil, fmt.Errorf("error decoding points: %s", err.Error())
	}
	return points, nil
}

func loadV1Cache(reader io.Reader) ([]kdbush.Point[geomodel.Info], error) {
	decoder := gob.NewDecoder(reader)
	var points []kdbush.Point[geomodel.Info]
	err := decoder.Decode(&points)
	if err != nil {
		return nil, fmt.Errorf("error decoding v1 cache: %s", err.Error())
	}
	return points, nil
}

package cachesaver

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"io"
	"log/slog"

	savev1 "github.com/royalcat/rgeocache/cachesaver/save/v1"
	saveproto "github.com/royalcat/rgeocache/cachesaver/save/v1/proto"
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/kdbush"
)

func LoadFromReader(reader io.Reader, log *slog.Logger) ([]kdbush.Point[geomodel.Info], error) {
	magic := make([]byte, len(MAGIC_BYTES))
	_, err := reader.Read(magic)
	if err != nil {
		return nil, fmt.Errorf("error reading magic bytes: %s", err.Error())
	}

	// If the magic bytes are not equal to the expected value, we assume it's a legacy format
	if string(magic) != string(MAGIC_BYTES) {
		log.Info("Magic bytes not detected, trying legacy format")
		return legacyLoader(io.MultiReader(bytes.NewReader(magic), reader))
	}

	var compatibilityLevel uint32
	err = binary.Read(reader, binary.LittleEndian, &compatibilityLevel)
	if err != nil {
		return nil, fmt.Errorf("error reading compatibility level: %s", err.Error())
	}

	switch compatibilityLevel {
	case savev1.COMPATIBILITY_LEVEL:
		log.Info("Loading v1 cache format")
		points, metadata, err := loadV1Cache(reader)
		if err != nil {
			return nil, err
		}
		if metadata != nil {
			log.Info("Loaded cache metadata", "version", metadata.Version, "locale", metadata.Locale, "date_created", metadata.DateCreated)
		}
		return points, nil
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

func loadV1Cache(reader io.Reader) ([]kdbush.Point[geomodel.Info], *saveproto.CacheMetadata, error) {
	cache, metadata, err := savev1.Load(reader)
	if err != nil {
		return nil, nil, fmt.Errorf("error loading v1 cache: %s", err.Error())
	}

	points := make([]kdbush.Point[geomodel.Info], len(cache.Points))
	for i, point := range cache.Points {
		points[i] = kdbush.Point[geomodel.Info]{
			X: point.Lat,
			Y: point.Lon,
			Data: geomodel.Info{
				Name:        point.Name,
				Street:      cache.Streets[point.Street],
				HouseNumber: point.HouseNumber,
				City:        cache.Cities[point.City],
				Region:      cache.Regions[point.Region],
				Weight:      point.Weight,
			},
		}
	}
	return points, metadata, nil
}

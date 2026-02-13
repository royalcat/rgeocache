package cachesaver

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	savev1 "github.com/royalcat/rgeocache/cachesaver/save/v1"
	"github.com/royalcat/rgeocache/kdbush"
)

func LoadFromReader(reader io.Reader, log *slog.Logger) ([]kdbush.Point[cachemodel.Info], []cachemodel.Zone, error) {
	magic := make([]byte, len(MAGIC_BYTES))
	_, err := reader.Read(magic)
	if err != nil {
		return nil, nil, fmt.Errorf("error reading magic bytes: %s", err.Error())
	}

	// If the magic bytes are not equal to the expected value, we assume it's a legacy format
	if string(magic) != string(MAGIC_BYTES) {
		log.Info("Magic bytes not detected, trying legacy format")
		points, err := legacyLoader(io.MultiReader(bytes.NewReader(magic), reader))
		if err != nil {
			return nil, nil, fmt.Errorf("error loading legacy cache: %s", err.Error())
		}
		return points, []cachemodel.Zone{}, nil
	}

	var compatibilityLevel uint32
	err = binary.Read(reader, binary.LittleEndian, &compatibilityLevel)
	if err != nil {
		return nil, nil, fmt.Errorf("error reading compatibility level: %s", err.Error())
	}

	switch compatibilityLevel {
	case savev1.COMPATIBILITY_LEVEL:
		log.Info("Loading v1 cache format")
		points, zones, metadata, err := loadV1Cache(reader)
		if err != nil {
			return nil, nil, fmt.Errorf("error loading v1 cache: %s", err.Error())
		}
		if metadata != nil {
			log.Info("Loaded cache metadata", "version", metadata.Version, "locale", metadata.Locale, "date_created", metadata.DateCreated)
		}
		return points, zones, nil
	}

	return nil, nil, fmt.Errorf("unsupported compatibility level: %d", compatibilityLevel)

}

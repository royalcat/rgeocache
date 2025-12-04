package geocoder

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"unique"

	"github.com/klauspost/compress/zstd"
	"github.com/royalcat/rgeocache/cachesaver"
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/kdbush"
)

func LoadGeoCoderFromReader(r io.Reader, opts ...Option) (*RGeoCoder, error) {
	options := loadOptions(opts...)
	log := options.logger

	log.Info("Loading geocoder points from reader")
	pointsRaw, err := cachesaver.LoadFromReader(r, log)
	if err != nil {
		return nil, fmt.Errorf("error loading points: %s", err.Error())
	}
	points := optimizePoints(pointsRaw)

	tree := kdbush.NewBush(points, 256)
	return newRGeoCoder(tree, opts...), nil
}

func LoadGeoCoderFromFile(file string, opts ...Option) (*RGeoCoder, error) {
	reader, err := openReader(file)
	if err != nil {
		return nil, fmt.Errorf("error opening points file: %s", err.Error())
	}

	return LoadGeoCoderFromReader(reader, opts...)
}

// Deprecated: Use LoadGeoCoderFromFile instead.
func (f *RGeoCoder) LoadFromPointsFile(file string) error {
	reader, err := openReader(file)
	if err != nil {
		return fmt.Errorf("error opening points file: %s", err.Error())
	}

	pointsRaw, err := cachesaver.LoadFromReader(reader, slog.Default())
	if err != nil {
		return fmt.Errorf("error loading points: %s", err.Error())
	}
	points := optimizePoints(pointsRaw)

	f.tree = kdbush.NewBush(points, 256)
	return nil
}

func newRGeoCoder(tree *kdbush.KDBush[*geoInfo], opts ...Option) *RGeoCoder {
	options := loadOptions(opts...)
	options.logger.Info("Initializing geocoder")

	return &RGeoCoder{
		tree:         tree,
		searchRadius: options.searchRadius,
		logger:       options.logger,
	}
}

func openReader(name string) (io.ReadCloser, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, fmt.Errorf("can`t open file error: %s", err.Error())
	}

	if strings.HasSuffix(name, ".zst") {
		dec, err := zstd.NewReader(file)
		if err != nil {
			return nil, fmt.Errorf("can`t create zstd reader: %s", err.Error())
		}

		return dec.IOReadCloser(), nil
	}

	return file, nil
}

func optimizePoints(points []kdbush.Point[geomodel.Info]) []kdbush.Point[*geoInfo] {
	result := make([]kdbush.Point[*geoInfo], len(points))
	for i, point := range points {
		result[i] = kdbush.Point[*geoInfo]{
			X: point.X, Y: point.Y,
			Data: &geoInfo{
				Name:        point.Data.Name,
				Street:      unique.Make(point.Data.Street),
				HouseNumber: unique.Make(point.Data.HouseNumber),
				City:        unique.Make(point.Data.City),
				Region:      unique.Make(point.Data.Region),
			},
		}
	}
	return result
}

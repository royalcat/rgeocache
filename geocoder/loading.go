package geocoder

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/royalcat/rgeocache/cachesaver"
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/kdbush"
)

func loadOptions(opts ...Option) options {
	options := options{
		searchRadius: maxSearchRadius,
		logger:       slog.Default(),
	}
	for _, o := range opts {
		o.apply(&options)
	}
	return options
}

func newRGeoCoder(tree *kdbush.KDBush[*geomodel.Info], opts ...Option) *RGeoCoder {
	options := loadOptions(opts...)
	options.logger.Info("Initializing geocoder")

	return &RGeoCoder{
		tree:         tree,
		searchRadius: options.searchRadius,
		logger:       options.logger,
	}
}

func NewRGeoCoder(opts ...Option) *RGeoCoder {
	return newRGeoCoder(kdbush.NewBush[*geomodel.Info](nil, 256), opts...)
}

func LoadGeoCoderFromReader(r io.Reader, opts ...Option) (*RGeoCoder, error) {
	options := loadOptions(opts...)
	log := options.logger

	log.Info("Loading geocoder points from reader")
	pointsRaw, err := cachesaver.LoadFromReader(r, log)
	if err != nil {
		return nil, fmt.Errorf("error loading points: %s", err.Error())
	}
	points := convertPointsDataToPointer(pointsRaw)

	tree := kdbush.NewBush(points, 256)
	return newRGeoCoder(tree, opts...), nil
}

func convertPointsDataToPointer[T any](points []kdbush.Point[T]) []kdbush.Point[*T] {
	result := make([]kdbush.Point[*T], len(points))
	for i, point := range points {
		result[i] = kdbush.Point[*T]{X: point.X, Y: point.Y, Data: &point.Data}
	}
	return result
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
	points := convertPointsDataToPointer(pointsRaw)

	f.tree = kdbush.NewBush(points, 256)
	return nil
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

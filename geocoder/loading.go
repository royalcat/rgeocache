package geocoder

import (
	"fmt"
	"io"
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
	}
	for _, o := range opts {
		o.apply(&options)
	}
	return options
}

func newRGeoCoder(tree *kdbush.KDBush[geomodel.Info], opts ...Option) *RGeoCoder {
	options := loadOptions(opts...)
	return &RGeoCoder{
		tree:         tree,
		searchRadius: options.searchRadius,
	}
}

func NewRGeoCoder(opts ...Option) *RGeoCoder {
	return newRGeoCoder(kdbush.NewBush[geomodel.Info](nil, 256), opts...)
}

func LoadGeoCoderFromReader(r io.Reader, opts ...Option) (*RGeoCoder, error) {
	points, err := cachesaver.LoadFromReader(r)
	if err != nil {
		return nil, fmt.Errorf("error loading points: %s", err.Error())
	}

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

	points, err := cachesaver.LoadFromReader(reader)
	if err != nil {
		return fmt.Errorf("error loading points: %s", err.Error())
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
		dec, err := zstd.NewReader(file)
		if err != nil {
			return nil, fmt.Errorf("can`t create zstd reader: %s", err.Error())
		}

		return dec.IOReadCloser(), nil
	}

	return file, nil
}

package geocoder

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"unique"

	"github.com/klauspost/compress/zstd"
	"github.com/royalcat/rgeocache/bordertree"
	"github.com/royalcat/rgeocache/cachesaver"
	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	"github.com/royalcat/rgeocache/kdbush"
)

func LoadGeoCoderFromReader(r io.Reader, opts ...Option) (*RGeoCoder, error) {
	options := loadOptions(opts...)
	log := options.logger

	log.Info("Loading geocoder points from reader")
	pointsRaw, zonesRaw, err := cachesaver.LoadFromReader(r, log)
	if err != nil {
		return nil, fmt.Errorf("error loading points: %s", err.Error())
	}

	points := optimizePoints(pointsRaw)
	tree := kdbush.NewBush(points, 256)

	regions := bordertree.NewBorderTree[unique.Handle[string]]()
	countries := bordertree.NewBorderTree[unique.Handle[string]]()
	for _, zone := range zonesRaw {
		switch zone.Type {
		case cachemodel.ZoneRegion:
			regions.InsertBorder(zone.Name, zone.Polygon)
		case cachemodel.ZoneCountry:
			countries.InsertBorder(zone.Name, zone.Polygon)
		}
	}

	return newRGeoCoder(tree, regions, countries, opts...), nil
}

func LoadGeoCoderFromFile(file string, opts ...Option) (*RGeoCoder, error) {
	reader, err := openReader(file)
	if err != nil {
		return nil, fmt.Errorf("error opening points file: %s", err.Error())
	}
	defer reader.Close()

	bufReader := bufio.NewReaderSize(reader, 4*1024*1024) // 4 MB
	return LoadGeoCoderFromReader(bufReader, opts...)
}

// Deprecated: Use LoadGeoCoderFromFile instead.
func (f *RGeoCoder) LoadFromPointsFile(file string) error {
	reader, err := openReader(file)
	if err != nil {
		return fmt.Errorf("error opening points file: %s", err.Error())
	}

	pointsRaw, zonesRaw, err := cachesaver.LoadFromReader(reader, slog.Default())
	if err != nil {
		return fmt.Errorf("error loading points: %s", err.Error())
	}

	points := optimizePoints(pointsRaw)
	f.tree = kdbush.NewBush(points, 256)

	f.regions = bordertree.NewBorderTree[unique.Handle[string]]()
	f.countries = bordertree.NewBorderTree[unique.Handle[string]]()
	for _, zone := range zonesRaw {
		switch zone.Type {
		case cachemodel.ZoneRegion:
			f.regions.InsertBorder(zone.Name, zone.Polygon)
		case cachemodel.ZoneCountry:
			f.countries.InsertBorder(zone.Name, zone.Polygon)
		}
	}

	return nil
}

func newRGeoCoder(tree *kdbush.KDBush[*geoInfo], regions *bordertree.BorderTree[unique.Handle[string]], countries *bordertree.BorderTree[unique.Handle[string]], opts ...Option) *RGeoCoder {
	options := loadOptions(opts...)
	options.logger.Info("Initializing geocoder")

	return &RGeoCoder{
		tree:         tree,
		regions:      regions,
		countries:    countries,
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

func optimizePoints(points []cachemodel.Point) []kdbush.Point[*geoInfo] {
	result := make([]kdbush.Point[*geoInfo], len(points))
	for i, point := range points {
		result[i] = kdbush.Point[*geoInfo]{
			X: point.X, Y: point.Y,
			Data: &geoInfo{
				Name:        point.Data.Name,
				Street:      point.Data.Street,
				HouseNumber: point.Data.HouseNumber,
				City:        point.Data.City,
				Region:      point.Data.Region,
				Weight:      uint8(point.Data.Weight),
			},
		}
	}
	return result
}

func PrintCacheSizeAnalysisForFile(file string) error {
	reader, err := openReader(file)
	if err != nil {
		return fmt.Errorf("error opening points file: %s", err.Error())
	}
	defer reader.Close()

	return cachesaver.PrintCacheSizeAnalysis(reader)
}

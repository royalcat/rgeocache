package cachesaver

import (
	"fmt"
	"io"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	savev2 "github.com/royalcat/rgeocache/cachesaver/save/v2"
	"github.com/royalcat/rgeocache/kdbush"
)

func loadV2Cache(reader io.Reader) ([]kdbush.Point[cachemodel.Info], []cachemodel.Zone, *cachemodel.Metadata, error) {
	pointsIter, zonesIter, metadata, err := savev2.Load(reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("error loading v2 cache: %w", err)
	}

	points := make([]kdbush.Point[cachemodel.Info], 0, 128)
	for point, err := range pointsIter {
		if err != nil {
			return nil, nil, nil, fmt.Errorf("error reading point: %w", err)
		}
		points = append(points, point)
	}

	zones := make([]cachemodel.Zone, 0, 128)
	for zone, err := range zonesIter {
		if err != nil {
			return nil, nil, nil, fmt.Errorf("error reading zone: %w", err)
		}
		zones = append(zones, zone)
	}

	return points, zones, metadata, nil
}

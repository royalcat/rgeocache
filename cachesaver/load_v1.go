package cachesaver

import (
	"fmt"
	"io"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	savev1 "github.com/royalcat/rgeocache/cachesaver/save/v1"
	"github.com/royalcat/rgeocache/kdbush"
)

func loadV1Cache(reader io.Reader) ([]kdbush.Point[cachemodel.Info], []cachemodel.Zone, *cachemodel.Metadata, error) {
	pointsIter, zonesIter, metadata, err := savev1.Load(reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("error loading v1 cache: %s", err.Error())
	}

	points := make([]kdbush.Point[cachemodel.Info], 0, 128)
	for point, err := range pointsIter {
		if err != nil {
			return nil, nil, nil, fmt.Errorf("error reading point: %s", err.Error())
		}
		points = append(points, point)
	}

	zones := make([]cachemodel.Zone, 0, 128)
	for zone, err := range zonesIter {
		if err != nil {
			return nil, nil, nil, fmt.Errorf("error reading zone: %s", err.Error())
		}
		zones = append(zones, zone)
	}

	return points, zones, metadata, nil
}

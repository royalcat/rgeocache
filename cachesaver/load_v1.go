package cachesaver

import (
	"fmt"
	"io"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	savev1 "github.com/royalcat/rgeocache/cachesaver/save/v1"
	"github.com/royalcat/rgeocache/kdbush"
)

func loadV1Cache(reader io.Reader) ([]kdbush.Point[cachemodel.Info], *cachemodel.Metadata, error) {
	pointsIter, _, metadata, err := savev1.Load(reader)
	if err != nil {
		return nil, nil, fmt.Errorf("error loading v1 cache: %s", err.Error())
	}

	points := make([]kdbush.Point[cachemodel.Info], 0, 128)
	for point, err := range pointsIter {
		if err != nil {
			return nil, nil, fmt.Errorf("error reading point: %s", err.Error())
		}
		points = append(points, point)
	}

	return points, metadata, nil
}

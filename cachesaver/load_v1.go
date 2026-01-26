package cachesaver

import (
	"fmt"
	"io"
	"unique"

	savev1 "github.com/royalcat/rgeocache/cachesaver/save/v1"
	saveproto "github.com/royalcat/rgeocache/cachesaver/save/v1/proto"
	"github.com/royalcat/rgeocache/kdbush"
)

func loadV1Cache(reader io.Reader) ([]kdbush.Point[Info], *saveproto.CacheMetadata, error) {
	pointsIter, strings, metadata, err := savev1.Load(reader)
	if err != nil {
		return nil, nil, fmt.Errorf("error loading v1 cache: %s", err.Error())
	}

	points := make([]kdbush.Point[Info], 0, 128)
	for point, err := range pointsIter {
		if err != nil {
			return nil, nil, fmt.Errorf("error reading point: %s", err.Error())
		}
		points = append(points, kdbush.Point[Info]{
			X: point.Lat,
			Y: point.Lon,
			Data: Info{
				Name:        point.Name,
				Street:      unique.Make(strings.Streets[point.Street]),
				HouseNumber: unique.Make(point.HouseNumber),
				City:        unique.Make(strings.Cities[point.City]),
				Region:      unique.Make(strings.Regions[point.Region]),
				Weight:      point.Weight,
			},
		})
	}
	return points, metadata, nil
}

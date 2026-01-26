package cachesaver

import (
	"encoding/gob"
	"fmt"
	"io"
	"unique"

	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/kdbush"
)

func legacyLoader(reader io.Reader) ([]kdbush.Point[Info], error) {
	decoder := gob.NewDecoder(reader)
	var points []kdbush.Point[geomodel.Info]
	err := decoder.Decode(&points)
	if err != nil {
		return nil, fmt.Errorf("error decoding points: %s", err.Error())
	}

	var out []kdbush.Point[Info]
	for _, point := range points {
		out = append(out, kdbush.Point[Info]{
			X: point.X, Y: point.Y,
			Data: Info{
				Name:        point.Data.Name,
				Street:      unique.Make(point.Data.Street),
				HouseNumber: unique.Make(point.Data.HouseNumber),
				City:        unique.Make(point.Data.City),
				Region:      unique.Make(point.Data.Region),
				Weight:      point.Data.Weight,
			},
		})
	}

	return out, nil
}

package cachesaver

import (
	"fmt"
	"io"
	"log/slog"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	savev1 "github.com/royalcat/rgeocache/cachesaver/save/v1"
	"github.com/royalcat/rgeocache/kdbush"
)

// graphOnlyRoadWeight is the generator weight of road shape points (weightRoad
// in geoparser). The v1 format has no geo type byte, so the weight is the only
// road marker a loaded cache carries.
const graphOnlyRoadWeight = 5

// skipGraphOnlyRoad reports whether a v1 point exists only to carry the v2 road
// graph. Such a point is a road shape point with neither a street nor a name,
// so it has no address to return; the region and country it stores are resolved
// from the border trees at query time anyway, and v1 never stores the graph.
// City is ignored on purpose: a road point without a street or name is graph
// geometry even when the settlement polygon it lies in was recorded on it.
func skipGraphOnlyRoad(info cachemodel.Info) bool {
	return info.Weight == graphOnlyRoadWeight &&
		info.Street.Value() == "" &&
		info.Name.Value() == ""
}

func loadV1Cache(reader io.Reader, log *slog.Logger) ([]kdbush.Point[cachemodel.Info], []cachemodel.Zone, *cachemodel.Metadata, error) {
	pointsIter, zonesIter, metadata, err := savev1.Load(reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("error loading v1 cache: %s", err.Error())
	}

	points := make([]kdbush.Point[cachemodel.Info], 0, 128)
	skipped := 0
	for point, err := range pointsIter {
		if err != nil {
			return nil, nil, nil, fmt.Errorf("error reading point: %s", err.Error())
		}
		if skipGraphOnlyRoad(point.Data) {
			skipped++
			continue
		}
		points = append(points, point)
	}
	if skipped > 0 {
		log.Info("Filtered nameless road points from v1 cache", "kept", len(points), "skipped", skipped)
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

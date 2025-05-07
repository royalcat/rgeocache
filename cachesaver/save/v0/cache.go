package savev0

import (
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/kdbush"
)

type Cache []kdbush.Point[geomodel.Info]

package cachesaver

import (
	"encoding/binary"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
)

var MAGIC_BYTES = []byte("RGEO")

var endianess = binary.LittleEndian

type Metadata = cachemodel.Metadata
type Zone = cachemodel.Zone
type Point = cachemodel.Point

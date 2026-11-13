package cachesaver

import (
	"encoding/binary"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	"google.golang.org/protobuf/proto"
)

// marshalOpts makes protobuf encoding byte-stable between runs.
var marshalOpts = proto.MarshalOptions{Deterministic: true}

var MAGIC_BYTES = []byte("RGEO")

var endianess = binary.LittleEndian

type Metadata = cachemodel.Metadata
type Zone = cachemodel.Zone
type Point = cachemodel.Point

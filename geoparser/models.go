package geoparser

import (
	"encoding/binary"
	"log/slog"
	"math"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/encoding/wkb"
)

type cachePoint orb.Point

func (p cachePoint) ToBytes() []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint64(b[:8], math.Float64bits(p[0]))
	binary.LittleEndian.PutUint64(b[8:], math.Float64bits(p[1]))
	return b
}

func (p cachePoint) FromBytes(b []byte) cachePoint {
	p[0] = math.Float64frombits(binary.LittleEndian.Uint64(b[:8]))
	p[1] = math.Float64frombits(binary.LittleEndian.Uint64(b[8:]))
	return p
}

type cacheWay orb.LineString

func (p cacheWay) ToBytes() []byte {
	data, err := wkb.Marshal(orb.LineString(p))
	if err != nil {
		slog.Error("error marshalling line string", "string", p, "error", err.Error())
	}

	return data
}

func (p cacheWay) FromBytes(b []byte) cacheWay {
	data, err := wkb.Unmarshal(b)
	if err != nil {
		slog.Error("error unmarshalling line string", "string", p, "error", err.Error())
	}
	way, _ := data.(orb.LineString)
	return cacheWay(way)
}

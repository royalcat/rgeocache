package geoparser

import (
	"encoding/binary"
	"log/slog"
	"math"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/encoding/wkb"
)

type cachePlace struct {
	Name         string
	Bound        orb.Bound
	MultiPolygon orb.MultiPolygon
}

type cachePoint orb.Point

func (p cachePoint) ToBytes() []byte {
	return nodePointByte(p[0], p[1])
}

func (p cachePoint) FromBytes(b []byte) cachePoint {
	p[0], p[1] = bytePoint(b)
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

func nodePointByte(x, y float64) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint64(b[:8], math.Float64bits(x))
	binary.LittleEndian.PutUint64(b[8:], math.Float64bits(y))
	return b
}

func bytePoint(b []byte) (x, y float64) {
	x = math.Float64frombits(binary.LittleEndian.Uint64(b[:8]))
	y = math.Float64frombits(binary.LittleEndian.Uint64(b[8:]))
	return x, y
}

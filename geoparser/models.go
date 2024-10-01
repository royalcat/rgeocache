package geoparser

import (
	"encoding/binary"
	"math"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/encoding/wkb"
	"github.com/sirupsen/logrus"
)

type cachePlace struct {
	Name          string
	LocalizedName string
	Bound         orb.Bound
	MultiPolygon  orb.MultiPolygon
}

func (c cachePlace) BestName() string {
	if c.LocalizedName != "" {
		return c.LocalizedName
	}
	return c.Name
}

type cacheHighway struct {
	Name          string
	LocalizedName string
}

func (c cacheHighway) BestName() string {
	if c.LocalizedName != "" {
		return c.LocalizedName
	}
	return c.Name
}

type cachePoint orb.Point

func (p cachePoint) ToBytes() []byte {
	return NodePointByte(p[0], p[1])
}

func (p cachePoint) FromBytes(b []byte) cachePoint {
	p[0], p[1] = BytePoint(b)
	return p
}

type cacheWay orb.LineString

func (p cacheWay) ToBytes() []byte {
	data, err := wkb.Marshal(orb.LineString(p))
	if err != nil {
		logrus.Errorf("error marshalling line string: %v with err: %s", p, err.Error())
	}

	return data
}

func (p cacheWay) FromBytes(b []byte) cacheWay {
	data, err := wkb.Unmarshal(b)
	if err != nil {
		logrus.Errorf("error unmarshalling line string: %v with err: %s", p, err.Error())
	}
	way, _ := data.(orb.LineString)
	return cacheWay(way)
}

func NodeIdKey(id int64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, uint64(id))
	return b
}

func NodePointByte(x, y float64) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint64(b[:8], math.Float64bits(x))
	binary.LittleEndian.PutUint64(b[8:], math.Float64bits(y))
	return b
}

func BytePoint(b []byte) (x, y float64) {
	x = math.Float64frombits(binary.LittleEndian.Uint64(b[:8]))
	y = math.Float64frombits(binary.LittleEndian.Uint64(b[8:]))
	return x, y
}

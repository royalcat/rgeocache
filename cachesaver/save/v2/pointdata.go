package savev2

import (
	"encoding"
	"encoding/binary"
	"fmt"
)

// Compile-time interface checks.
var (
	_ encoding.BinaryMarshaler   = V2PointData{}
	_ encoding.BinaryUnmarshaler = (*V2PointData)(nil)
)

// V2PointData is the on-disk representation of a point's address data.
// Each string field stores a byte offset and length into the raw string blob
// section of the v2 cache file. Strings are read lazily from the mmap'd file
// only when a point is matched.
//
// Total size: 61 bytes (5×(int64+uint32) + uint8 + 3 padding).
type V2PointData struct {
	NameOffset        int64
	NameLen           uint32
	StreetOffset      int64
	StreetLen         uint32
	HouseNumberOffset int64
	HouseNumberLen    uint32
	CityOffset        int64
	CityLen           uint32
	RegionOffset      int64
	RegionLen         uint32
	Weight            uint8
}

const v2PointDataSize = 61

// strOff is a helper for packing offset+length during save.
type strOff struct {
	offset int64
	length uint32
}

// MarshalBinary implements encoding.BinaryMarshaler (value receiver).
func (d V2PointData) MarshalBinary() ([]byte, error) {
	buf := make([]byte, v2PointDataSize)
	binary.LittleEndian.PutUint64(buf[0:8], uint64(d.NameOffset))
	binary.LittleEndian.PutUint32(buf[8:12], d.NameLen)
	binary.LittleEndian.PutUint64(buf[12:20], uint64(d.StreetOffset))
	binary.LittleEndian.PutUint32(buf[20:24], d.StreetLen)
	binary.LittleEndian.PutUint64(buf[24:32], uint64(d.HouseNumberOffset))
	binary.LittleEndian.PutUint32(buf[32:36], d.HouseNumberLen)
	binary.LittleEndian.PutUint64(buf[36:44], uint64(d.CityOffset))
	binary.LittleEndian.PutUint32(buf[44:48], d.CityLen)
	binary.LittleEndian.PutUint64(buf[48:56], uint64(d.RegionOffset))
	binary.LittleEndian.PutUint32(buf[56:60], d.RegionLen)
	buf[60] = d.Weight
	return buf, nil
}

// UnmarshalBinary implements encoding.BinaryUnmarshaler (pointer receiver).
func (d *V2PointData) UnmarshalBinary(data []byte) error {
	if len(data) == 0 {
		*d = V2PointData{}
		return nil
	}
	if len(data) < v2PointDataSize {
		return fmt.Errorf("savev2: invalid V2PointData size: got %d, want %d", len(data), v2PointDataSize)
	}
	d.NameOffset = int64(binary.LittleEndian.Uint64(data[0:8]))
	d.NameLen = binary.LittleEndian.Uint32(data[8:12])
	d.StreetOffset = int64(binary.LittleEndian.Uint64(data[12:20]))
	d.StreetLen = binary.LittleEndian.Uint32(data[20:24])
	d.HouseNumberOffset = int64(binary.LittleEndian.Uint64(data[24:32]))
	d.HouseNumberLen = binary.LittleEndian.Uint32(data[32:36])
	d.CityOffset = int64(binary.LittleEndian.Uint64(data[36:44]))
	d.CityLen = binary.LittleEndian.Uint32(data[44:48])
	d.RegionOffset = int64(binary.LittleEndian.Uint64(data[48:56]))
	d.RegionLen = binary.LittleEndian.Uint32(data[56:60])
	d.Weight = data[60]
	return nil
}

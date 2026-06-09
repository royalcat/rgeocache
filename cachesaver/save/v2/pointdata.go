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
// Each string field stores a uint32 ID that indexes into the static string index
// (offset index + null-terminated string data block). Strings are read lazily
// from the mmap'd file only when a point is matched.
//
// Total size: 21 bytes (5×uint32 + uint8).
// ID 0 represents the empty string.
type V2PointData struct {
	NameID        uint32
	StreetID      uint32
	HouseNumberID uint32
	CityID        uint32
	RegionID      uint32
	Weight        uint8
}

const v2PointDataSize = 21

// MarshalBinary implements encoding.BinaryMarshaler (value receiver).
func (d V2PointData) MarshalBinary() ([]byte, error) {
	buf := make([]byte, v2PointDataSize)
	binary.LittleEndian.PutUint32(buf[0:4], d.NameID)
	binary.LittleEndian.PutUint32(buf[4:8], d.StreetID)
	binary.LittleEndian.PutUint32(buf[8:12], d.HouseNumberID)
	binary.LittleEndian.PutUint32(buf[12:16], d.CityID)
	binary.LittleEndian.PutUint32(buf[16:20], d.RegionID)
	buf[20] = d.Weight
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
	d.NameID = binary.LittleEndian.Uint32(data[0:4])
	d.StreetID = binary.LittleEndian.Uint32(data[4:8])
	d.HouseNumberID = binary.LittleEndian.Uint32(data[8:12])
	d.CityID = binary.LittleEndian.Uint32(data[12:16])
	d.RegionID = binary.LittleEndian.Uint32(data[16:20])
	d.Weight = data[20]
	return nil
}

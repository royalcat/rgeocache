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
// Total size: 22 bytes (5×uint32 + uint8 weight + uint8 geo type).
// ID 0 represents the empty string.
//
// The geo type byte was appended to the original 21-byte record without bumping
// the compat level: older readers ignore the trailing byte, and newer readers
// treat a 21-byte blob as a legacy record with an unknown (zero) type.
type V2PointData struct {
	NameID        uint32
	StreetID      uint32
	HouseNumberID uint32
	CityID        uint32
	RegionID      uint32
	Weight        uint8
	GeoType       uint8
}

const (
	// v2PointDataSize is the current on-disk record size.
	v2PointDataSize = 22
	// v2PointDataLegacySize is the size before the geo type byte was appended.
	// A record this short is still valid; its GeoType is left as 0 (unknown).
	v2PointDataLegacySize = 21
)

// MarshalBinary implements encoding.BinaryMarshaler (value receiver).
func (d V2PointData) MarshalBinary() ([]byte, error) {
	buf := make([]byte, v2PointDataSize)
	binary.LittleEndian.PutUint32(buf[0:4], d.NameID)
	binary.LittleEndian.PutUint32(buf[4:8], d.StreetID)
	binary.LittleEndian.PutUint32(buf[8:12], d.HouseNumberID)
	binary.LittleEndian.PutUint32(buf[12:16], d.CityID)
	binary.LittleEndian.PutUint32(buf[16:20], d.RegionID)
	buf[20] = d.Weight
	buf[21] = d.GeoType
	return buf, nil
}

// UnmarshalBinary implements encoding.BinaryUnmarshaler (pointer receiver).
//
// It accepts both the current 22-byte records and legacy 21-byte records; the
// latter leave GeoType at 0 (unknown).
func (d *V2PointData) UnmarshalBinary(data []byte) error {
	if len(data) == 0 {
		*d = V2PointData{}
		return nil
	}
	if len(data) < v2PointDataLegacySize {
		return fmt.Errorf("savev2: invalid V2PointData size: got %d, want at least %d", len(data), v2PointDataLegacySize)
	}
	d.NameID = binary.LittleEndian.Uint32(data[0:4])
	d.StreetID = binary.LittleEndian.Uint32(data[4:8])
	d.HouseNumberID = binary.LittleEndian.Uint32(data[8:12])
	d.CityID = binary.LittleEndian.Uint32(data[12:16])
	d.RegionID = binary.LittleEndian.Uint32(data[16:20])
	d.Weight = data[20]
	if len(data) >= v2PointDataSize {
		d.GeoType = data[21]
	} else {
		d.GeoType = 0
	}
	return nil
}

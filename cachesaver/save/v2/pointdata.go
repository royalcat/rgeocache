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
// It stores indices into the StringsTableV2 string tables for compactness
// and fast O(1) resolution at query time.
//
// Total size: 21 bytes (5×4 uint32 + 1 uint8).
type V2PointData struct {
	NameStrIdx        uint32
	StreetStrIdx      uint32
	HouseNumberStrIdx uint32
	CityStrIdx        uint32
	RegionStrIdx      uint32
	Weight            uint8
}

const v2PointDataSize = 21

// MarshalBinary implements encoding.BinaryMarshaler (value receiver).
// This satisfies the V constraint of kdbush.BuildDisk.
func (d V2PointData) MarshalBinary() ([]byte, error) {
	buf := make([]byte, v2PointDataSize)
	d.marshalTo(buf)
	return buf, nil
}

// marshalTo writes the binary representation into buf (must be at least v2PointDataSize bytes).
func (d V2PointData) marshalTo(buf []byte) {
	binary.LittleEndian.PutUint32(buf[0:4], d.NameStrIdx)
	binary.LittleEndian.PutUint32(buf[4:8], d.StreetStrIdx)
	binary.LittleEndian.PutUint32(buf[8:12], d.HouseNumberStrIdx)
	binary.LittleEndian.PutUint32(buf[12:16], d.CityStrIdx)
	binary.LittleEndian.PutUint32(buf[16:20], d.RegionStrIdx)
	buf[20] = d.Weight
}

// UnmarshalBinary implements encoding.BinaryUnmarshaler (pointer receiver).
// This satisfies the VP constraint of kdbush.DiskKDBush.
func (d *V2PointData) UnmarshalBinary(data []byte) error {
	if len(data) == 0 {
		*d = V2PointData{}
		return nil
	}
	if len(data) < v2PointDataSize {
		return fmt.Errorf("savev2: invalid V2PointData size: got %d, want %d", len(data), v2PointDataSize)
	}
	d.NameStrIdx = binary.LittleEndian.Uint32(data[0:4])
	d.StreetStrIdx = binary.LittleEndian.Uint32(data[4:8])
	d.HouseNumberStrIdx = binary.LittleEndian.Uint32(data[8:12])
	d.CityStrIdx = binary.LittleEndian.Uint32(data[12:16])
	d.RegionStrIdx = binary.LittleEndian.Uint32(data[16:20])
	d.Weight = data[20]
	return nil
}

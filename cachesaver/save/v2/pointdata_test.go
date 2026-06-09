package savev2

import (
	"testing"
)

func TestV2PointDataRoundTrip(t *testing.T) {
	orig := V2PointData{
		NameID:        1,
		StreetID:      2,
		HouseNumberID: 3,
		CityID:        4,
		RegionID:      5,
		Weight:        10,
	}

	data, err := orig.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary failed: %v", err)
	}

	if len(data) != v2PointDataSize {
		t.Fatalf("expected %d bytes, got %d", v2PointDataSize, len(data))
	}

	var decoded V2PointData
	err = decoded.UnmarshalBinary(data)
	if err != nil {
		t.Fatalf("UnmarshalBinary failed: %v", err)
	}

	if decoded != orig {
		t.Fatalf("round-trip mismatch: %+v != %+v", decoded, orig)
	}
}

func TestV2PointDataUnmarshalEmpty(t *testing.T) {
	var d V2PointData
	err := d.UnmarshalBinary(nil)
	if err != nil {
		t.Fatalf("UnmarshalBinary nil failed: %v", err)
	}
	if d != (V2PointData{}) {
		t.Fatalf("expected zero value, got %+v", d)
	}

	err = d.UnmarshalBinary([]byte{})
	if err != nil {
		t.Fatalf("UnmarshalBinary empty failed: %v", err)
	}
	if d != (V2PointData{}) {
		t.Fatalf("expected zero value, got %+v", d)
	}
}

func TestV2PointDataUnmarshalShort(t *testing.T) {
	var d V2PointData
	err := d.UnmarshalBinary(make([]byte, 10))
	if err == nil {
		t.Fatal("expected error for short data, got nil")
	}
}

func TestV2PointDataZeroValues(t *testing.T) {
	orig := V2PointData{Weight: 5}
	data, err := orig.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary failed: %v", err)
	}

	var decoded V2PointData
	err = decoded.UnmarshalBinary(data)
	if err != nil {
		t.Fatalf("UnmarshalBinary failed: %v", err)
	}

	if decoded != orig {
		t.Fatalf("round-trip mismatch: %+v != %+v", decoded, orig)
	}
}

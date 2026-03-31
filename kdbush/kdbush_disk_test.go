package kdbush

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"slices"
	"testing"
)

// ---------------------------------------------------------------------------
// testData — a minimal type satisfying the binaryPointer constraint
// ---------------------------------------------------------------------------

type testData struct {
	Value int
	Label [8]byte
}

// MarshalBinary implements encoding.BinaryMarshaler (value receiver so that
// testData itself satisfies encoding.BinaryMarshaler, as required by BuildDisk).
func (d testData) MarshalBinary() ([]byte, error) {
	buf := make([]byte, 8+8)
	binary.LittleEndian.PutUint64(buf[0:8], uint64(d.Value))
	copy(buf[8:16], d.Label[:])
	return buf, nil
}

// UnmarshalBinary implements encoding.BinaryUnmarshaler (pointer receiver).
func (d *testData) UnmarshalBinary(data []byte) error {
	if len(data) == 0 {
		*d = testData{}
		return nil
	}
	d.Value = int(binary.LittleEndian.Uint64(data[0:8]))
	copy(d.Label[:], data[8:16])
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func makeLabel(i int) [8]byte {
	var l [8]byte
	binary.LittleEndian.PutUint64(l[:], uint64(i*7+3))
	return l
}

func generateTestPoints(n int) []Point[testData] {
	rng := rand.New(rand.NewSource(42))
	pts := make([]Point[testData], n)
	for i := range pts {
		pts[i] = Point[testData]{
			X:    rng.Float64() * 1000,
			Y:    rng.Float64() * 1000,
			Data: testData{Value: i, Label: makeLabel(i)},
		}
	}
	return pts
}

func buildAndOpen(t *testing.T, pts []Point[testData], nodeSize int) *DiskKDBush[testData, *testData] {
	t.Helper()
	var buf bytes.Buffer
	_, err := BuildDisk[testData, *testData](pts, nodeSize, &buf)
	if err != nil {
		t.Fatalf("BuildDisk: %v", err)
	}
	disk, err := OpenDisk[testData, *testData](bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("OpenDisk: %v", err)
	}
	return disk
}

// collectWithin runs an in-memory Within and returns matched original indices,
// which we compare against the disk version.
func collectMemWithin(bush *KDBush[testData], qx, qy, radius float64) []int {
	var idxs []int
	bush.Within(qx, qy, radius, func(p Point[testData]) bool {
		idxs = append(idxs, p.Data.Value) // Data.Value == original index
		return true
	})
	return idxs
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestDisk_RoundTrip_Range(t *testing.T) {
	pts := generateTestPoints(10_000)
	bush := NewBush(pts, 64)
	disk := buildAndOpen(t, pts, 64)

	if disk.NumPoints() != 10_000 {
		t.Fatalf("NumPoints: got %d, want 10000", disk.NumPoints())
	}
	if disk.NodeSize() != 64 {
		t.Fatalf("NodeSize: got %d, want 64", disk.NodeSize())
	}

	queries := [][4]float64{
		{200, 200, 800, 800},
		{0, 0, 100, 100},
		{900, 900, 1000, 1000},
		{0, 0, 1000, 1000},         // everything
		{500, 500, 500.01, 500.01}, // tiny box
	}

	for _, q := range queries {
		memResult := bush.Range(q[0], q[1], q[2], q[3])
		diskResult, err := disk.Range(q[0], q[1], q[2], q[3])
		if err != nil {
			t.Fatalf("disk.Range(%v): %v", q, err)
		}

		// Extract original indices from disk result Data.Value
		diskIdxs := make([]int, len(diskResult))
		for i, p := range diskResult {
			diskIdxs[i] = p.Data.Value
		}

		slices.Sort(memResult)
		slices.Sort(diskIdxs)

		if !slices.Equal(memResult, diskIdxs) {
			t.Errorf("Range(%v): mem returned %d results, disk returned %d",
				q, len(memResult), len(diskIdxs))
		}
	}
}

func TestDisk_RoundTrip_Within(t *testing.T) {
	pts := generateTestPoints(10_000)
	bush := NewBush(pts, 64)
	disk := buildAndOpen(t, pts, 64)

	type withinQuery struct {
		qx, qy, radius float64
	}

	queries := []withinQuery{
		{500, 500, 100},
		{0, 0, 50},
		{999, 999, 10},
		{500, 500, 1500},  // covers everything
		{500, 500, 0.001}, // tiny radius
	}

	for _, q := range queries {
		memIdxs := collectMemWithin(bush, q.qx, q.qy, q.radius)

		var diskIdxs []int
		err := disk.Within(q.qx, q.qy, q.radius, func(p Point[testData]) bool {
			diskIdxs = append(diskIdxs, p.Data.Value)
			return true
		})
		if err != nil {
			t.Fatalf("disk.Within(%v): %v", q, err)
		}

		slices.Sort(memIdxs)
		slices.Sort(diskIdxs)

		if !slices.Equal(memIdxs, diskIdxs) {
			t.Errorf("Within(%.1f, %.1f, %.1f): mem=%d disk=%d",
				q.qx, q.qy, q.radius, len(memIdxs), len(diskIdxs))
		}
	}
}

func TestDisk_DataIntegrity(t *testing.T) {
	pts := []Point[testData]{
		{X: 10, Y: 20, Data: testData{Value: 100, Label: makeLabel(100)}},
		{X: 30, Y: 40, Data: testData{Value: 200, Label: makeLabel(200)}},
		{X: 50, Y: 60, Data: testData{Value: 300, Label: makeLabel(300)}},
	}
	disk := buildAndOpen(t, pts, 64)

	result, err := disk.Range(0, 0, 100, 100)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result))
	}

	// Build a map from (X,Y) → expected Data for verification.
	type key struct{ x, y float64 }
	expected := map[key]testData{
		{10, 20}: pts[0].Data,
		{30, 40}: pts[1].Data,
		{50, 60}: pts[2].Data,
	}

	for _, p := range result {
		want, ok := expected[key{p.X, p.Y}]
		if !ok {
			t.Errorf("unexpected point at (%.0f, %.0f)", p.X, p.Y)
			continue
		}
		if p.Data.Value != want.Value {
			t.Errorf("point (%.0f,%.0f): Data.Value = %d, want %d",
				p.X, p.Y, p.Data.Value, want.Value)
		}
		if p.Data.Label != want.Label {
			t.Errorf("point (%.0f,%.0f): Data.Label mismatch", p.X, p.Y)
		}
	}
}

func TestDisk_WithinDataIntegrity(t *testing.T) {
	pts := generateTestPoints(1_000)
	disk := buildAndOpen(t, pts, 64)

	err := disk.Within(500, 500, 50, func(p Point[testData]) bool {
		origIdx := p.Data.Value
		if origIdx < 0 || origIdx >= len(pts) {
			t.Errorf("invalid original index %d", origIdx)
			return false
		}
		orig := pts[origIdx]
		if p.X != orig.X || p.Y != orig.Y {
			t.Errorf("coords mismatch for idx %d: got (%.4f,%.4f) want (%.4f,%.4f)",
				origIdx, p.X, p.Y, orig.X, orig.Y)
		}
		if p.Data.Label != orig.Data.Label {
			t.Errorf("Label mismatch for idx %d", origIdx)
		}
		return true
	})
	if err != nil {
		t.Fatalf("Within: %v", err)
	}
}

func TestDisk_EmptyIndex(t *testing.T) {
	disk := buildAndOpen(t, nil, 64)

	if disk.NumPoints() != 0 {
		t.Fatalf("NumPoints: got %d, want 0", disk.NumPoints())
	}

	result, err := disk.Range(0, 0, 1000, 1000)
	if err != nil {
		t.Fatalf("Range on empty: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("Range on empty returned %d results", len(result))
	}

	err = disk.Within(500, 500, 100, func(p Point[testData]) bool {
		t.Error("Within handler called on empty index")
		return true
	})
	if err != nil {
		t.Fatalf("Within on empty: %v", err)
	}
}

func TestDisk_SinglePoint(t *testing.T) {
	pts := []Point[testData]{
		{X: 42, Y: 24, Data: testData{Value: 7, Label: makeLabel(7)}},
	}
	disk := buildAndOpen(t, pts, 64)

	result, err := disk.Range(0, 0, 100, 100)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("Range: got %d results, want 1", len(result))
	}
	if result[0].Data.Value != 7 {
		t.Errorf("Data.Value = %d, want 7", result[0].Data.Value)
	}

	// Outside the box
	result, err = disk.Range(100, 100, 200, 200)
	if err != nil {
		t.Fatalf("Range outside: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("Range outside: got %d results, want 0", len(result))
	}

	var count int
	err = disk.Within(42, 24, 1, func(p Point[testData]) bool {
		count++
		if p.Data.Value != 7 {
			t.Errorf("Within Data.Value = %d, want 7", p.Data.Value)
		}
		return true
	})
	if err != nil {
		t.Fatalf("Within: %v", err)
	}
	if count != 1 {
		t.Errorf("Within: got %d matches, want 1", count)
	}
}

func TestDisk_WithinEarlyStop(t *testing.T) {
	pts := generateTestPoints(10_000)
	disk := buildAndOpen(t, pts, 64)

	count := 0
	err := disk.Within(500, 500, 500, func(p Point[testData]) bool {
		count++
		return count < 3
	})
	if err != nil {
		t.Fatalf("Within early stop: %v", err)
	}
	if count != 3 {
		t.Errorf("Within early stop: got %d calls, want 3", count)
	}
}

func TestDisk_InvalidMagic(t *testing.T) {
	data := make([]byte, diskHeaderSize)
	copy(data[0:4], []byte("NOPE"))

	_, err := OpenDisk[testData, *testData](bytes.NewReader(data))
	if err == nil {
		t.Fatal("expected error for invalid magic bytes")
	}
}

func TestDisk_InvalidVersion(t *testing.T) {
	data := make([]byte, diskHeaderSize)
	copy(data[0:4], diskMagic[:])
	diskByteOrder.PutUint32(data[4:8], 99)

	_, err := OpenDisk[testData, *testData](bytes.NewReader(data))
	if err == nil {
		t.Fatal("expected error for unsupported version")
	}
}

func TestDisk_TruncatedHeader(t *testing.T) {
	_, err := OpenDisk[testData, *testData](bytes.NewReader([]byte("KDB")))
	if err == nil {
		t.Fatal("expected error for truncated header")
	}
}

func TestDisk_VariousNodeSizes(t *testing.T) {
	pts := generateTestPoints(1_000)
	nodeSizes := []int{1, 4, 16, 64, 256}

	for _, ns := range nodeSizes {
		bush := NewBush(pts, ns)
		disk := buildAndOpen(t, pts, ns)

		minX, minY, maxX, maxY := 300.0, 300.0, 700.0, 700.0
		memResult := bush.Range(minX, minY, maxX, maxY)
		diskResult, err := disk.Range(minX, minY, maxX, maxY)
		if err != nil {
			t.Fatalf("nodeSize=%d Range: %v", ns, err)
		}

		diskIdxs := make([]int, len(diskResult))
		for i, p := range diskResult {
			diskIdxs[i] = p.Data.Value
		}

		slices.Sort(memResult)
		slices.Sort(diskIdxs)

		if !slices.Equal(memResult, diskIdxs) {
			t.Errorf("nodeSize=%d Range: mem=%d disk=%d", ns, len(memResult), len(diskIdxs))
		}
	}
}

func TestDisk_WrittenSize(t *testing.T) {
	n := 500
	pts := generateTestPoints(n)

	var buf bytes.Buffer
	written, err := BuildDisk[testData, *testData](pts, 32, &buf)
	if err != nil {
		t.Fatalf("BuildDisk: %v", err)
	}

	// Each testData marshals to 16 bytes.
	blobBytes := int64(n) * 16
	expectedSize := int64(diskHeaderSize) +
		int64(n)*8 + // idxs
		int64(n)*16 + // coords
		int64(n+1)*8 + // data offset table
		blobBytes // data blobs

	if written != expectedSize {
		t.Errorf("BuildDisk returned %d bytes, expected %d", written, expectedSize)
	}
	if int64(buf.Len()) != expectedSize {
		t.Errorf("buffer has %d bytes, expected %d", buf.Len(), expectedSize)
	}
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

func BenchmarkDisk(b *testing.B) {
	rng := rand.New(rand.NewSource(42))
	n := 100_000
	pts := make([]Point[testData], n)
	for i := range pts {
		pts[i] = Point[testData]{
			X:    rng.Float64() * 1000,
			Y:    rng.Float64() * 1000,
			Data: testData{Value: i, Label: makeLabel(i)},
		}
	}

	bush := NewBush(pts, 64)

	var buf bytes.Buffer
	if _, err := BuildDisk[testData, *testData](pts, 64, &buf); err != nil {
		b.Fatalf("BuildDisk: %v", err)
	}
	disk, err := OpenDisk[testData, *testData](bytes.NewReader(buf.Bytes()))
	if err != nil {
		b.Fatalf("OpenDisk: %v", err)
	}

	b.Run("Range/mem", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			bush.Range(200, 200, 800, 800)
		}
	})

	b.Run("Range/disk", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			disk.Range(200, 200, 800, 800)
		}
	})

	b.Run("Within/mem", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			bush.Within(500, 500, 100, func(p Point[testData]) bool {
				return true
			})
		}
	})

	b.Run("Within/disk", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			disk.Within(500, 500, 100, func(p Point[testData]) bool {
				return true
			})
		}
	})
}

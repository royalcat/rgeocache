package savev2

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/dustin/go-humanize"
	savev2proto "github.com/royalcat/rgeocache/cachesaver/save/v2/proto"
	"google.golang.org/protobuf/proto"
)

// printCacheSizeAnalysisFromHeader prints the non-KDBH parts from the header.
func printCacheSizeAnalysisFromHeader(header *savev2proto.V2Header) {
	fmt.Printf("Metadata size: %s\n", humanize.Bytes(uint64(header.MetadataSize)))
	fmt.Printf("Strings blob size: %s\n", humanize.Bytes(uint64(header.StringsBlobSize)))
	fmt.Printf("Zones size: %s\n", humanize.Bytes(uint64(header.ZonesSize)))
}

// PrintCacheAnalysis reads a v2 cache from r and prints a human-readable size
// breakdown. The reader must be positioned immediately after the magic bytes and
// compatibility level (i.e., at the first byte of the header size field).
func PrintCacheAnalysis(r io.Reader) error {
	// 1. Read V2Header.
	var headerSize uint32
	if err := binary.Read(r, binary.LittleEndian, &headerSize); err != nil {
		return fmt.Errorf("v2 analyze: failed to read header size: %w", err)
	}

	headerBytes := make([]byte, headerSize)
	if _, err := io.ReadFull(r, headerBytes); err != nil {
		return fmt.Errorf("v2 analyze: failed to read header: %w", err)
	}
	var header savev2proto.V2Header
	if err := proto.Unmarshal(headerBytes, &header); err != nil {
		return fmt.Errorf("v2 analyze: failed to unmarshal header: %w", err)
	}

	// Header size itself (the uint32 we just read + the proto blob)
	headerOverhead := uint64(4 + headerSize)

	printCacheSizeAnalysisFromHeader(&header)

	// 2. Skip metadata + string blob + zones to reach the KDBH block.
	skipSize := int64(header.MetadataSize) + int64(header.StringsBlobSize) + int64(header.ZonesSize)
	if _, err := io.CopyN(io.Discard, r, skipSize); err != nil {
		return fmt.Errorf("v2 analyze: failed to skip to KDBH: %w", err)
	}

	// 3. Read KDBH header (32 bytes).
	var kdbhHeader [32]byte
	if _, err := io.ReadFull(r, kdbhHeader[:]); err != nil {
		return fmt.Errorf("v2 analyze: failed to read KDBH header: %w", err)
	}
	var diskMagic [4]byte
	copy(diskMagic[:], kdbhHeader[0:4])
	if string(diskMagic[:]) != "KDBH" {
		return fmt.Errorf("v2 analyze: invalid KDBH magic %q", diskMagic[:])
	}
	nodeSize := binary.LittleEndian.Uint64(kdbhHeader[8:16])
	numPoints := binary.LittleEndian.Uint64(kdbhHeader[16:24])

	// 4. Skip tree indices (N*8) + tree coordinates (N*16) to reach the offset table.
	treeSize := int64(numPoints*8 + numPoints*16)
	if _, err := io.CopyN(io.Discard, r, treeSize); err != nil {
		return fmt.Errorf("v2 analyze: failed to skip KDBH tree: %w", err)
	}

	// 5. Read the data offset table ((N+1)*8 bytes).
	offsetCount := numPoints + 1
	offsetTable := make([]int64, offsetCount)
	for i := range offsetCount {
		var buf [8]byte
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return fmt.Errorf("v2 analyze: failed to read offset[%d]: %w", i, err)
		}
		offsetTable[i] = int64(binary.LittleEndian.Uint64(buf[:]))
	}
	totalBlobSize := offsetTable[numPoints] // last cumulative offset = total blob data size

	// 6. Compute and print KDBH breakdown.
	indicesSize := numPoints * 8
	coordsSize := numPoints * 16
	offsetsSize := offsetCount * 8
	kdbhTotal := uint64(32 + indicesSize + coordsSize + offsetsSize) + uint64(totalBlobSize)

	fmt.Printf("Points count: %d (node size: %d)\n", numPoints, nodeSize)
	fmt.Printf("  Tree indices: %s\n", humanize.Bytes(indicesSize))
	fmt.Printf("  Tree coords:  %s\n", humanize.Bytes(coordsSize))
	fmt.Printf("  Data offsets: %s\n", humanize.Bytes(offsetsSize))
	fmt.Printf("  Data blobs:   %s\n", humanize.Bytes(uint64(totalBlobSize)))
	fmt.Printf("Points (KDBH) total: %s\n", humanize.Bytes(kdbhTotal))

	// 7. Grand total.
	totalSize := headerOverhead +
		uint64(header.MetadataSize) +
		uint64(header.StringsBlobSize) +
		uint64(header.ZonesSize) +
		kdbhTotal
	fmt.Printf("Total uncompressed size: %s\n", humanize.Bytes(totalSize))

	return nil
}

package savev1

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/dustin/go-humanize"
	saveproto "github.com/royalcat/rgeocache/cachesaver/save/v1/proto"
)

// print cache size analysis in human-readable format
func printCacheSizeAnalysisFromHeader(header *saveproto.CacheHeader) {
	fmt.Printf("Metadata size: %s\n", humanize.Bytes(uint64(header.MetadataSize)))
	fmt.Printf("Strings cache size: %s\n", humanize.Bytes(uint64(header.StringsCacheSize)))

	var totalPointsBlobSize uint64
	for _, blobSize := range header.PointsBlobSizes {
		totalPointsBlobSize += uint64(blobSize)
	}
	var totalZonesBlobSize uint64
	for _, blobSize := range header.ZonesBlobSizes {
		totalZonesBlobSize += uint64(blobSize)
	}
	fmt.Printf("Total points size: %s\n", humanize.Bytes(totalPointsBlobSize))
	fmt.Printf("Total zones size: %s\n", humanize.Bytes(totalZonesBlobSize))

	totalSize := uint64(header.MetadataSize) + uint64(header.StringsCacheSize) + totalPointsBlobSize + totalZonesBlobSize
	fmt.Printf("Total uncompressed size: %s\n", humanize.Bytes(totalSize))

}

// Cache stats in human-readable format
func PrintCacheAnalysis(r io.Reader) error {
	var headerSize uint32
	err := binary.Read(r, binary.LittleEndian, &headerSize)
	if err != nil {
		return fmt.Errorf("failed to read header size: %w", err)
	}

	var header saveproto.CacheHeader
	err = readToProto(r, headerSize, &header)
	if err != nil {
		return fmt.Errorf("failed to read header: %w", err)
	}

	printCacheSizeAnalysisFromHeader(&header)
	return nil
}

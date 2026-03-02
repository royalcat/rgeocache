package savev1

import (
	"fmt"

	"github.com/dustin/go-humanize"
	saveproto "github.com/royalcat/rgeocache/cachesaver/save/v1/proto"
)

// print cache size analysis in human-readable format
func PrintCacheSizeAnalysis(header *saveproto.CacheHeader) {
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

package cachesaver

import (
	"encoding/binary"
	"io"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	savev1 "github.com/royalcat/rgeocache/cachesaver/save/v1"
)

func Save(points []cachemodel.Point, zones []cachemodel.Zone, meta cachemodel.Metadata, w io.Writer) error {
	_, err := w.Write(MAGIC_BYTES)
	if err != nil {
		return err
	}

	err = binary.Write(w, binary.LittleEndian, savev1.COMPATIBILITY_LEVEL)
	if err != nil {
		return err
	}

	err = savev1.Save(w, points, zones, meta)
	if err != nil {
		return err
	}
	return nil
}

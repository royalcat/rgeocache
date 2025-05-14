package cachesaver

import (
	"encoding/binary"
	"io"

	savev1 "github.com/royalcat/rgeocache/cachesaver/save/v1"
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/kdbush"
)

func Save(points []kdbush.Point[geomodel.Info], w io.Writer) error {
	_, err := w.Write(MAGIC_BYTES)
	if err != nil {
		return err
	}

	err = binary.Write(w, binary.LittleEndian, savev1.COMPATIBILITY_LEVEL)
	if err != nil {
		return err
	}

	err = savev1.Save(w, savev1.CacheFromPoints(points))
	if err != nil {
		return err
	}
	return nil
}

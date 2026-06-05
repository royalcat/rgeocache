package cachesaver

import (
	"encoding/binary"
	"io"
	"iter"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	savev1 "github.com/royalcat/rgeocache/cachesaver/save/v1"
	savev2 "github.com/royalcat/rgeocache/cachesaver/save/v2"
)

func SaveV1(points iter.Seq[cachemodel.Point], zones iter.Seq[cachemodel.Zone], meta cachemodel.Metadata, w io.Writer) error {
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

// SaveV2 writes a v2 cache file with the mmap-compatible KDBH spatial index.
func Save(points iter.Seq[cachemodel.Point], zones iter.Seq[cachemodel.Zone], meta cachemodel.Metadata, w io.Writer) error {
	_, err := w.Write(MAGIC_BYTES)
	if err != nil {
		return err
	}

	err = binary.Write(w, binary.LittleEndian, savev2.COMPATIBILITY_LEVEL)
	if err != nil {
		return err
	}

	return savev2.Save(w, points, zones, meta)
}

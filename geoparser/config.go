package geoparser

import (
	"runtime"
	"time"
)

type Config struct {
	Threads               int
	Version               uint32
	PreferredLocalization string

	// SpoolDir is the directory used for the on-disk parse spool. Empty means
	// a temporary directory that is removed after generation.
	SpoolDir string

	// DateCreated is the creation timestamp written to the cache metadata. The
	// zero value means "now". Pin it to make the whole cache file reproducible
	// byte for byte.
	DateCreated time.Time
}

func ConfigDefault() Config {
	return Config{
		Threads:               runtime.GOMAXPROCS(-1),
		Version:               1,
		PreferredLocalization: "",
	}
}

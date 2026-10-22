package geoparser

import "runtime"

type Config struct {
	Threads               int
	Version               uint32
	PreferredLocalization string

	// SpoolDir is the directory used for the on-disk parse spool. Empty means
	// a temporary directory that is removed after generation.
	SpoolDir string
}

func ConfigDefault() Config {
	return Config{
		Threads:               runtime.GOMAXPROCS(-1),
		Version:               1,
		PreferredLocalization: "",
	}
}

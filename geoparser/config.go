package geoparser

import "runtime"

type Config struct {
	Threads               int
	Version               uint32
	CacheFormat           string // "1" or "2" — controls v1 vs v2 cache format
	PreferredLocalization string
	HighwayPointsDistance float64
}

func ConfigDefault() Config {
	return Config{
		Threads:               runtime.GOMAXPROCS(-1),
		Version:               1,
		PreferredLocalization: "",
		HighwayPointsDistance: 150,
	}
}

package geoparser

import "runtime"

type Config struct {
	Threads               int
	PreferredLocalization string
	HighwayPointsDistance float64
}

func ConfigDefault() Config {
	return Config{
		Threads:               runtime.GOMAXPROCS(-1),
		PreferredLocalization: "",
		HighwayPointsDistance: 100,
	}
}

package geoparser

import "runtime"

type Config struct {
	Threads               int
	Version               uint32
	PreferredLocalization string
}

func ConfigDefault() Config {
	return Config{
		Threads:               runtime.GOMAXPROCS(-1),
		Version:               1,
		PreferredLocalization: "",
	}
}

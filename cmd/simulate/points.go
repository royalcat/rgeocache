package main

import (
	"math/rand/v2"
	"time"
)

func generatePoints(n int, minLat, maxLat, minLon, maxLon float64) [][2]float64 {
	points := make([][2]float64, n)
	for i := range n {
		points[i] = [2]float64{
			minLat + rand.Float64()*(maxLat-minLat),
			minLon + rand.Float64()*(maxLon-minLon),
		}
	}
	return points
}

const defaultTimeout = 30 * time.Second

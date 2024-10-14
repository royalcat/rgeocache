package kv

const point32Size = 8

type point32 [2]float32

func castToPoint64[V ~[2]float64](v point32) V {
	return V([2]float64{float64(v[0]), float64(v[1])})
}

func castToPoint32[V ~[2]float64](v V) point32 {
	return [2]float32{float32(v[0]), float32(v[1])}
}

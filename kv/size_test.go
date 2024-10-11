package kv_test

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/paulmach/orb"
)

func TestMapSize(t *testing.T) {
	const mapSize = 20_000_000
	{
		m := make(map[uint64]orb.Point, mapSize)
		for i := range mapSize {
			m[uint64(i)] = orb.Point{float64(i), float64(i)}
		}
		alloc := allocatedMemory()
		fmt.Printf("uint64 to orb.Point: %d MiB, per element %d B\n", bToMb(alloc), alloc/mapSize)
	}
	runtime.GC()
	{
		m := make(map[int64]orb.Point, mapSize)
		for i := range mapSize {
			m[int64(i)] = orb.Point{float64(i), float64(i)}
		}
		alloc := allocatedMemory()
		fmt.Printf("uint64 to [2]float64: %d MiB, per element %d B\n", bToMb(alloc), alloc/mapSize)
	}
	runtime.GC()
	{
		m := make(map[int64]orb.Point, mapSize)
		for i := range mapSize {
			m[int64(i)] = orb.Point{float64(i), float64(i)}
		}
		alloc := allocatedMemory()
		fmt.Printf("int64 to orb.Point: %d MiB, per element %d B\n", bToMb(alloc), alloc/mapSize)
	}
	runtime.GC()
	{
		m := make(map[uint64][2]float32, mapSize)
		for i := range mapSize {
			m[uint64(i)] = [2]float32{float32(i), float32(i)}
		}
		alloc := allocatedMemory()
		fmt.Printf("uint64 to [2]float32: %d MiB, per element %d B\n", bToMb(alloc), alloc/mapSize)
	}
	runtime.GC()
	{
		m := make(map[uint64]orb.Point, mapSize)
		for i := range mapSize {
			m[uint64(i)] = orb.Point{float64(i), float64(i)}
		}
		alloc := allocatedMemory()
		fmt.Printf("int64 to orb.Point: %d MiB, per element %d B\n", bToMb(alloc), alloc/mapSize)
	}
	runtime.GC()
	{
		type block struct {
			a uint32
			b uint32
		}
		m := make(map[uint64]block, mapSize)
		for i := range mapSize {
			m[uint64(i)] = block{uint32(i), uint32(i)}
		}
		alloc := allocatedMemory()
		fmt.Printf("int64 to 2 uint32 struct: %d MiB, per element %d B\n", bToMb(alloc), alloc/mapSize)
	}
	runtime.GC()
	{
		m := make(map[int64]uint32, mapSize)
		for i := range mapSize {
			m[int64(i)] = uint32(i)
		}
		alloc := allocatedMemory()
		fmt.Printf("int64 to uint32: %d MiB, per element %d B\n", bToMb(alloc), alloc/mapSize)
	}
	runtime.GC()

}

var m runtime.MemStats

func allocatedMemory() uint64 {
	runtime.ReadMemStats(&m)
	return m.Alloc
}

func bToMb(b uint64) uint64 {
	return b / 1024 / 1024
}

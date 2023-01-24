package kdbush

import (
	"math"

	"github.com/royalcat/btrgo/btrcontainer"
)

// Minimal struct, that implements Point interface
type Point[T any] struct {
	X, Y float64
	//	Prev, Next *Point[T]
	Data T
}

type Lane[T any] struct {
	list *btrcontainer.List[Point[T]]
}

func NewLane[T any]() *Lane[T] {
	return &Lane[T]{
		list: btrcontainer.New[Point[T]](),
	}
}

func (l *Lane[T]) Add(p Point[T]) {
	l.list.PushBack(p)
}

type KDBush[T any] struct {
	NodeSize int
	Points   []Point[T]

	idxs   []int     //array of indexes
	coords []float64 //array of coordinates
}

func NewBush[T any](points []Point[T], nodeSize int) *KDBush[T] {
	b := KDBush[T]{}
	b.buildIndex(points, nodeSize)
	return &b
}

// Finds all items within the given bounding box and returns an array of indices that refer to the items in the original points input slice.
func (bush *KDBush[T]) Range(minX, minY, maxX, maxY float64) []int {
	stack := []int{0, len(bush.idxs) - 1, 0}
	result := []int{}
	var x, y float64

	for len(stack) > 0 {
		axis := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		right := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		left := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if right-left <= bush.NodeSize {
			for i := left; i <= right; i++ {
				x = bush.coords[2*i]
				y = bush.coords[2*i+1]
				if x >= minX && x <= maxX && y >= minY && y <= maxY {
					result = append(result, bush.idxs[i])
				}
			}
			continue
		}

		m := floor(float64(left+right) / 2.0)

		x = bush.coords[2*m]
		y = bush.coords[2*m+1]

		if x >= minX && x <= maxX && y >= minY && y <= maxY {
			result = append(result, bush.idxs[m])
		}

		nextAxis := (axis + 1) % 2

		if (axis == 0 && minX <= x) || (axis != 0 && minY <= y) {
			stack = append(stack, left)
			stack = append(stack, m-1)
			stack = append(stack, nextAxis)
		}

		if (axis == 0 && maxX >= x) || (axis != 0 && maxY >= y) {
			stack = append(stack, m+1)
			stack = append(stack, right)
			stack = append(stack, nextAxis)
		}

	}
	return result
}

func (bush *KDBush[T]) Within(qx, qy float64, radius float64, handler func(p Point[T]) bool) {
	stack := []int{0, len(bush.idxs) - 1, 0}
	r2 := radius * radius

	for len(stack) > 0 {
		axis := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		right := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		left := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if right-left <= bush.NodeSize {
			for i := left; i <= right; i++ {
				dst := sqrtDist(bush.coords[2*i], bush.coords[2*i+1], qx, qy)
				if dst <= r2 {
					if !handler(bush.Points[bush.idxs[i]]) {
						return
					}
				}
			}
			continue
		}

		m := floor(float64(left+right) / 2.0)
		x := bush.coords[2*m]
		y := bush.coords[2*m+1]

		if sqrtDist(x, y, qx, qy) <= r2 {
			if !handler(bush.Points[bush.idxs[m]]) {
				return
			}
		}

		nextAxis := (axis + 1) % 2

		if (axis == 0 && (qx-radius <= x)) || (axis != 0 && (qy-radius <= y)) {
			stack = append(stack, left)
			stack = append(stack, m-1)
			stack = append(stack, nextAxis)
		}

		if (axis == 0 && (qx+radius >= x)) || (axis != 0 && (qy+radius >= y)) {
			stack = append(stack, m+1)
			stack = append(stack, right)
			stack = append(stack, nextAxis)
		}
	}
}

///// private method to sort the data

////////////////////////////////////////////////////////////////
/// Sorting stuff
////////////////////////////////////////////////////////////////

func (bush *KDBush[T]) buildIndex(points []Point[T], nodeSize int) {
	bush.NodeSize = nodeSize
	bush.Points = points

	bush.idxs = make([]int, len(points))
	bush.coords = make([]float64, 2*len(points))

	for i, v := range points {
		bush.idxs[i] = i
		bush.coords[i*2] = v.X
		bush.coords[i*2+1] = v.Y
	}

	sort(bush.idxs, bush.coords, bush.NodeSize, 0, len(bush.idxs)-1, 0)
}

func sort(idxs []int, coords []float64, nodeSize int, left, right, depth int) {
	if (right - left) <= nodeSize {
		return
	}

	m := floor(float64(left+right) / 2.0)

	sselect(idxs, coords, m, left, right, depth%2)

	sort(idxs, coords, nodeSize, left, m-1, depth+1)
	sort(idxs, coords, nodeSize, m+1, right, depth+1)

}

func sselect(idxs []int, coords []float64, k, left, right, inc int) {
	//whatever you want
	for right > left {
		if (right - left) > 600 {
			n := right - left + 1
			m := k - left + 1
			z := math.Log(float64(n))
			s := 0.5 * math.Exp(2.0*z/3.0)
			sds := 1.0
			if float64(m)-float64(n)/2.0 < 0 {
				sds = -1.0
			}
			n_s := float64(n) - s
			sd := 0.5 * math.Sqrt(z*s*n_s/float64(n)) * sds
			newLeft := iMax(left, floor(float64(k)-float64(m)*s/float64(n)+sd))
			newRight := iMin(right, floor(float64(k)+float64(n-m)*s/float64(n)+sd))
			sselect(idxs, coords, k, newLeft, newRight, inc)
		}

		t := coords[2*k+inc]
		i := left
		j := right

		swapItem(idxs, coords, left, k)
		if coords[2*right+inc] > t {
			swapItem(idxs, coords, left, right)
		}

		for i < j {
			swapItem(idxs, coords, i, j)
			i += 1
			j -= 1
			for coords[2*i+inc] < t {
				i += 1
			}
			for coords[2*j+inc] > t {
				j -= 1
			}
		}

		if coords[2*left+inc] == t {
			swapItem(idxs, coords, left, j)
		} else {
			j += 1
			swapItem(idxs, coords, j, right)
		}

		if j <= k {
			left = j + 1
		}
		if k <= j {
			right = j - 1
		}
	}
}

func swapItem(idxs []int, coords []float64, i, j int) {
	swapi(idxs, i, j)
	swapf(coords, 2*i, 2*j)
	swapf(coords, 2*i+1, 2*j+1)
}

func swapf(a []float64, i, j int) {
	t := a[i]
	a[i] = a[j]
	a[j] = t
}

func swapi(a []int, i, j int) {
	t := a[i]
	a[i] = a[j]
	a[j] = t
}

func iMax(a, b int) int {
	if a > b {
		return a
	} else {
		return b
	}
}

func iMin(a, b int) int {
	if a < b {
		return a
	} else {
		return b
	}
}

func floor(in float64) int {
	out := math.Floor(in)
	return int(out)
}

func sqrtDist(ax, ay, bx, by float64) float64 {
	dx := ax - bx
	dy := ay - by
	return dx*dx + dy*dy
}

func orthoDist(ax, ay, bx, by float64) float64 {
	var la1, lo1, la2, lo2, r float64
	r = 6378100              // Earth radius in METERS (equator)
	lo1 = ax * math.Pi / 180 // longitude
	la1 = ay * math.Pi / 180 // latitude
	lo2 = bx * math.Pi / 180
	la2 = by * math.Pi / 180

	h := hsin(lo2-lo1) + math.Cos(lo1)*math.Cos(lo2)*hsin(la2-la1)

	return 2 * r * math.Asin(math.Sqrt(h))
}

func distance(ax, ay, bx, by float64, haversine bool) float64 {
	if haversine {
		return orthoDist(ax, ay, bx, by)
	}
	return sqrtDist(ax, ay, bx, by)
}

func hsin(theta float64) float64 {
	return math.Pow(math.Sin(theta/2), 2)
}

package savev1

type uniqueMap struct {
	m map[string]int
	i int
}

func newUniqueMap() *uniqueMap {
	return &uniqueMap{
		m: make(map[string]int),
		i: 0,
	}
}

func (uq *uniqueMap) Add(val string) int {
	i, ok := uq.m[val]
	if !ok {
		uq.m[val] = uq.i
		uq.i++
		return uq.i - 1
	}
	return i
}

func (uq *uniqueMap) Get(val string) int {
	if i, ok := uq.m[val]; ok {
		return i
	}
	return -1
}

func (uq *uniqueMap) Slice() []string {
	s := make([]string, uq.i)
	for k, v := range uq.m {
		s[v] = k
	}
	return s
}

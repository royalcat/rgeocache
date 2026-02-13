package savev1

type uniqueMap struct {
	m map[string]int
	i int
}

func newUniqueMap() *uniqueMap {
	return &uniqueMap{
		m: map[string]int{
			"": 0,
		},
		i: 0,
	}
}

func (uq *uniqueMap) Add(val string) int {
	i, ok := uq.m[val]
	if !ok {
		uq.i++
		uq.m[val] = uq.i
		return uq.i
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
	s := make([]string, uq.i+1)
	for v, i := range uq.m {
		s[i] = v
	}
	return s
}

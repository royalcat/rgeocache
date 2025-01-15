package main

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestUnmarshalPointsListFast(t *testing.T) {
	tests := []struct {
		data []byte
		want [][2]float64
	}{
		{[]byte(`[]`), [][2]float64{}},
		{[]byte(`[[1, 2]]`), [][2]float64{{1, 2}}},
		{[]byte(`[[1,2], [3,4]]`), [][2]float64{{1, 2}, {3, 4}}},
		{[]byte(`[[1,2.1], [3,4]]`), [][2]float64{{1, 2.1}, {3, 4}}},
		{[]byte(`[[-1.4, -1],[-0, 1]]`), [][2]float64{{-1.4, -1}, {0, 1}}},
		{[]byte(`[[1.4, 0.1], [3.1, -1]]`), [][2]float64{{1.4, 0.1}, {3.1, -1}}},
	}

	for _, tt := range tests {
		var res [][2]float64
		err := unmarshalPointsListFast(tt.data, &res)
		if err != nil {
			t.Fatalf("unexpected error: %s", err.Error())
		}

		if !slices.Equal(tt.want, res) {
			t.Fatalf("result expected %v; got %v", tt.want, res)
		}
	}
}

func FuzzUnmarshalPointsListFast(f *testing.F) {
	f.Add([]byte(`[]`))
	f.Add([]byte(`[[1,2]]`))
	f.Add([]byte(`[[1,2],[3,4]]`))
	f.Add([]byte(`[[1,2.1],[3]]`))
	f.Add([]byte(`[[1,2.1],[3,4,5]]`))
	f.Add([]byte(`[[1.4],[3.1]]`))
	f.Add([]byte(`[[-1.4, -1],[-0, 1]]`))
	f.Add([]byte(`[[a, -0],[0, 2]]`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var jsonRes [][2]float64
		jsonErr := json.Unmarshal(data, &jsonRes)

		var fastRes [][2]float64
		fastErr := unmarshalPointsListFast(data, &fastRes)
		if jsonErr != nil && fastErr == nil {
			t.Fatalf("json invalid,  json error: %s, error should not be nil", jsonErr.Error())
		}

		// if !slices.Equal(jsonRes, fastRes) {
		// 	t.Fatalf("result expected %v; got %v", jsonRes, fastRes)
		// }
	})
}

func BenchmarkTestUnmarshalPoints(b *testing.B) {
	data := []byte(`[[1,2], [3,4], [5,6], [7,8], [9,10]]`)

	b.Run("json", func(b *testing.B) {
		var res [][2]float64
		for i := 0; i < b.N; i++ {
			err := json.Unmarshal(data, &res)
			if err != nil {
				b.Fatalf("unexpected error: %s", err.Error())
			}
		}
	})

	b.Run("fast", func(b *testing.B) {
		var res [][2]float64
		for i := 0; i < b.N; i++ {
			err := unmarshalPointsListFast(data, &res)
			if err != nil {
				b.Fatalf("unexpected error: %s", err.Error())
			}
		}
	})

}

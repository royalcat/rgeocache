package server

import (
	"fmt"
	"slices"
	"strconv"
)

func unmarshalPointsListFast(data []byte, result *[][2]float64) error {
	i := 0
	n := len(data)

	*result = slices.Grow(*result, n/16) // n/16 is a heuristic

	// Skip leading whitespace
	for i < n && (data[i] == ' ' || data[i] == '\n' || data[i] == '\t' || data[i] == '\r') {
		i++
	}

	if i >= n || data[i] != '[' {
		return fmt.Errorf("invalid format: expected '['")
	}
	i++

	for i < n {
		// Skip whitespace
		for i < n && (data[i] == ' ' || data[i] == '\n' || data[i] == '\t' || data[i] == '\r') {
			i++
		}

		if i < n && data[i] == ']' {
			i++
			break
		}

		if i >= n || data[i] != '[' {
			return fmt.Errorf("invalid format: expected '[' for point")
		}
		i++

		var point [2]float64
		for j := 0; j < 2; j++ {
			// Skip whitespace
			for i < n && (data[i] == ' ' || data[i] == '\n' || data[i] == '\t' || data[i] == '\r') {
				i++
			}

			start := i
			// Find the end of the number
			for i < n && ((data[i] >= '0' && data[i] <= '9') || data[i] == '-' || data[i] == '.' || data[i] == 'e' || data[i] == 'E') {
				i++
			}
			if start == i {
				point[j] = 0
			} else {
				num, err := strconv.ParseFloat(string(data[start:i]), 64)
				if err != nil {
					return fmt.Errorf("invalid number: %v", err)
				}
				point[j] = num
			}

			// Skip whitespace
			for i < n && (data[i] == ' ' || data[i] == '\n' || data[i] == '\t' || data[i] == '\r') {
				i++
			}

			if j < 1 {
				if i < n && data[i] == ',' {
					i++
				} else {
					return fmt.Errorf("invalid format: expected ',' between coordinates")
				}
			}
		}

		// After two numbers, skip to end of point
		for i < n && data[i] != ']' {
			i++
		}
		if i >= n || data[i] != ']' {
			return fmt.Errorf("invalid format: expected ']' at end of point")
		}
		i++

		*result = append(*result, point)

		// Skip whitespace
		for i < n && (data[i] == ' ' || data[i] == '\n' || data[i] == '\t' || data[i] == '\r') {
			i++
		}

		// Check for comma or end
		if i < n && data[i] == ',' {
			i++
			continue
		} else if i < n && data[i] == ']' {
			i++
			break
		}
	}

	return nil
}

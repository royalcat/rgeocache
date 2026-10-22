// Package strtab interns strings and builds the v2 cache string table.
package strtab

import "fmt"

// Table assigns sequential ids to unique strings. Id 0 is reserved for the
// empty string. Tables are not safe for concurrent use.
type Table struct {
	m    map[string]uint32
	byID []string
}

// New returns a table containing only the empty string at id 0.
func New() *Table {
	return &Table{
		m:    map[string]uint32{"": 0},
		byID: []string{""},
	}
}

// Add returns the id of s, assigning a new one when s was not seen before.
func (t *Table) Add(s string) uint32 {
	if id, ok := t.m[s]; ok {
		return id
	}
	id := uint32(len(t.byID))
	t.m[s] = id
	t.byID = append(t.byID, s)
	return id
}

// String returns the string stored at id, or "" when id is out of range.
func (t *Table) String(id uint32) string {
	if int(id) >= len(t.byID) {
		return ""
	}
	return t.byID[id]
}

// Len returns the number of stored strings, including the empty string.
func (t *Table) Len() int {
	return len(t.byID)
}

// Strings returns the table in id order. The caller must not modify it.
func (t *Table) Strings() []string {
	return t.byID
}

// Build produces the v2 string offset index and the null-terminated string
// data block.
func (t *Table) Build() ([]uint32, []byte, error) {
	n := len(t.byID)
	index := make([]uint32, n)

	pos := uint64(0)
	for id, s := range t.byID {
		if pos > uint64(^uint32(0)) {
			return nil, nil, fmt.Errorf("strtab: string table exceeds 4 GiB")
		}
		index[id] = uint32(pos)
		pos += uint64(len(s)) + 1 // +1 for the NUL terminator
	}
	if pos > uint64(^uint32(0)) {
		return nil, nil, fmt.Errorf("strtab: string table exceeds 4 GiB")
	}

	data := make([]byte, pos)
	for id, s := range t.byID {
		copy(data[index[id]:], s)
		data[index[id]+uint32(len(s))] = 0
	}
	return index, data, nil
}

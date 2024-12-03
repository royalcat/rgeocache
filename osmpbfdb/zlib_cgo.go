//go:build cgo
// +build cgo

package osmpbfdb

import (
	"bytes"
	"io"

	"github.com/datadog/czlib"
)

func zlibReader(data []byte) (io.ReadCloser, error) {
	return czlib.NewReader(bytes.NewReader(data))
}

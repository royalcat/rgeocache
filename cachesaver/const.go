package cachesaver

import "encoding/binary"

var MAGIC_BYTES = []byte("RGEO")

var endianess = binary.LittleEndian

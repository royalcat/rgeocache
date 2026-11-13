package savev2

import "google.golang.org/protobuf/proto"

const COMPATIBILITY_LEVEL uint32 = 2

// marshalOpts makes protobuf encoding byte-stable between runs.
var marshalOpts = proto.MarshalOptions{Deterministic: true}

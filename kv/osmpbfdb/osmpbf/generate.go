package osmpbf

//go:generate protoc  --proto_path=. --go_opt=module=github.com/royalcat/rgeocache/kv/osmpbfdb/osmpbf  --go_out=.  fileformat.proto osmformat.proto

package osmpbfdb

//go:generate protoc  --proto_path=. --go_opt=module=github.com/royalcat/rgeocache/kv/osmpbfdb/osmproto  --go_out=osmproto osmproto/fileformat.proto osmproto/osmformat.proto

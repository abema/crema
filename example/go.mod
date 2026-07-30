module github.com/abema/crema/example

go 1.25.0

require (
	github.com/abema/crema v1.1.1
	github.com/abema/crema/ext/madvfree v1.1.1
	github.com/abema/crema/ext/protobuf v1.1.1
	github.com/abema/crema/ext/ristretto v1.1.1
	github.com/abema/crema/ext/rueidis v1.1.1
	github.com/abema/crema/ext/valkey-go v1.1.1
	github.com/redis/rueidis v1.0.76
	github.com/valkey-io/valkey-go v1.0.76
	google.golang.org/protobuf v1.36.11
)

require github.com/dgraph-io/ristretto v0.2.0 // indirect

replace github.com/abema/crema/ext/madvfree => ../ext/madvfree

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgraph-io/ristretto/v2 v2.4.2
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

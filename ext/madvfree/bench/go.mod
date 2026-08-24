module github.com/abema/crema/ext/madvfree/bench

go 1.25.0

require (
	github.com/abema/crema v1.2.0
	github.com/abema/crema/ext/madvfree v0.0.0
	github.com/abema/crema/ext/ristretto v1.2.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgraph-io/ristretto/v2 v2.4.2
	github.com/dustin/go-humanize v1.0.1 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace github.com/abema/crema/ext/madvfree => ..

module github.com/abema/crema/ext/valkey-go

go 1.24.9

require (
	github.com/abema/crema v0.0.0
	github.com/alicebob/miniredis/v2 v2.36.1
	github.com/valkey-io/valkey-go v1.0.70
)

require (
	github.com/yuin/gopher-lua v1.1.1 // indirect
	golang.org/x/sys v0.39.0 // indirect
)

replace github.com/abema/crema => ../..

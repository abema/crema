# ext/go-json

JSON serialization codec for `crema` using `goccy/go-json`.

## Features

- `JSONByteStringCodec` for encoding/decoding cache objects via goccy/go-json

## Usage

```go
codec := gojson.JSONByteStringCodec[MyValue]{}
```

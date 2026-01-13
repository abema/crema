package crema

import (
	"bytes"
	"encoding/json"
)

// SerializationCodec encodes and decodes cache objects to storage values.
// Implementations must be safe for concurrent use by multiple goroutines.
type SerializationCodec[V any, S any] interface {
	// Encode returns the cache object encoded into storage value.
	Encode(value CacheObject[V]) (S, error)
	// Decode reads the storage value into a cache object.
	Decode(data S) (CacheObject[V], error)
}

// NoopSerializationCodec passes CacheObject values through without encoding.
type NoopSerializationCodec[V any] struct{}

var _ SerializationCodec[any, CacheObject[any]] = NoopSerializationCodec[any]{}

// Encode copies the cache object.
func (n NoopSerializationCodec[V]) Encode(value CacheObject[V]) (CacheObject[V], error) {
	return value, nil
}

// Decode copies the cache object.
func (n NoopSerializationCodec[V]) Decode(data CacheObject[V]) (CacheObject[V], error) {
	return data, nil
}

// JSONByteStringCodec marshals cache objects as JSON bytes.
type JSONByteStringCodec[V any] struct{}

var _ SerializationCodec[any, []byte] = JSONByteStringCodec[any]{}

// Encode marshals the cache object into JSON bytes without a trailing newline.
func (j JSONByteStringCodec[V]) Encode(value CacheObject[V]) ([]byte, error) {
	buf := bytes.NewBuffer(nil)
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b, nil
}

// Decode unmarshals JSON bytes into a cache object.
func (j JSONByteStringCodec[V]) Decode(data []byte) (CacheObject[V], error) {
	var out CacheObject[V]
	if err := json.Unmarshal(data, &out); err != nil {
		return CacheObject[V]{}, err
	}
	return out, nil
}

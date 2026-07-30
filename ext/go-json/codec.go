package gojson

import (
	"bytes"

	"github.com/abema/crema"
	json "github.com/goccy/go-json"
)

// JSONByteStringCodec marshals cache objects as JSON bytes via goccy/go-json.
type JSONByteStringCodec[V any] struct{}

var (
	_ crema.CacheStorageCodec[any, []byte] = JSONByteStringCodec[any]{}
	_ crema.BufferReleasePolicy            = JSONByteStringCodec[any]{}
	_ crema.BufferEncoder[any]             = JSONByteStringCodec[any]{}
)

// Encode marshals the cache object into JSON bytes without a trailing newline.
func (j JSONByteStringCodec[V]) Encode(value crema.CacheObject[V]) ([]byte, error) {
	buf := bytes.NewBuffer(nil)
	if err := j.EncodeTo(buf, value); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// EncodeTo appends the cache object as JSON bytes to buf without a trailing newline.
func (j JSONByteStringCodec[V]) EncodeTo(buf *bytes.Buffer, value crema.CacheObject[V]) error {
	offset := buf.Len()
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		buf.Truncate(offset)

		return err
	}
	if b := buf.Bytes(); len(b) > offset && b[len(b)-1] == '\n' {
		buf.Truncate(buf.Len() - 1)
	}

	return nil
}

// Decode unmarshals JSON bytes into a cache object.
func (j JSONByteStringCodec[V]) Decode(data []byte) (crema.CacheObject[V], error) {
	var out crema.CacheObject[V]
	if err := json.Unmarshal(data, &out); err != nil {
		return crema.CacheObject[V]{}, err
	}

	return out, nil
}

func (j JSONByteStringCodec[V]) CanReleaseBufferOnDecode() bool {
	return true
}

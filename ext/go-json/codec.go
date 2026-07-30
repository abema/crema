package gojson

import (
	"bytes"
	"sync"

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

type jsonEncodeBuffer struct {
	buf *bytes.Buffer
	enc *json.Encoder
}

const maxPooledJSONEncodeBufferBytes = 64 << 10

var jsonEncodeBufferPool = sync.Pool{
	New: func() any {
		buf := bytes.NewBuffer(nil)
		enc := json.NewEncoder(buf)
		enc.SetEscapeHTML(false)

		return &jsonEncodeBuffer{buf: buf, enc: enc}
	},
}

func acquireJSONEncodeBuffer() *jsonEncodeBuffer {
	eb := jsonEncodeBufferPool.Get().(*jsonEncodeBuffer)
	eb.buf.Reset()

	return eb
}

func releaseJSONEncodeBuffer(eb *jsonEncodeBuffer) {
	if eb.buf.Cap() > maxPooledJSONEncodeBufferBytes {
		return
	}
	eb.buf.Reset()
	jsonEncodeBufferPool.Put(eb)
}

// Encode marshals the cache object into JSON bytes without a trailing newline.
func (j JSONByteStringCodec[V]) Encode(value crema.CacheObject[V]) ([]byte, error) {
	eb := acquireJSONEncodeBuffer()
	defer releaseJSONEncodeBuffer(eb)
	if err := eb.enc.Encode(value); err != nil {
		return nil, err
	}

	encoded := eb.buf.Bytes()
	if len(encoded) > 0 && encoded[len(encoded)-1] == '\n' {
		encoded = encoded[:len(encoded)-1]
	}

	return bytes.Clone(encoded), nil
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

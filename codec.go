package crema

import (
	"bytes"
	"compress/zlib"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// CacheStorageCodec encodes and decodes cache objects to storage values.
// Implementations must be safe for concurrent use by multiple goroutines.
type CacheStorageCodec[V any, S any] interface {
	// Encode returns the cache object encoded into storage value.
	Encode(value CacheObject[V]) (S, error)
	// Decode reads the storage value into a cache object.
	Decode(data S) (CacheObject[V], error)
}

// BufferReleasePolicy declares whether Decode can safely release buffer-backed input.
type BufferReleasePolicy interface {
	CanReleaseBufferOnDecode() bool
}

// BufferEncoder encodes a cache object into a caller-provided buffer.
// On error, EncodeTo must restore buf's original length.
type BufferEncoder[V any] interface {
	// EncodeTo appends the encoded cache object to buf.
	EncodeTo(buf *bytes.Buffer, value CacheObject[V]) error
}

// NoopCacheStorageCodec passes CacheObject values through without encoding.
type NoopCacheStorageCodec[V any] struct{}

var _ CacheStorageCodec[any, CacheObject[any]] = NoopCacheStorageCodec[any]{}

// Encode copies the cache object.
func (n NoopCacheStorageCodec[V]) Encode(value CacheObject[V]) (CacheObject[V], error) {
	return value, nil
}

// Decode copies the cache object.
func (n NoopCacheStorageCodec[V]) Decode(data CacheObject[V]) (CacheObject[V], error) {
	return data, nil
}

// JSONByteStringCodec marshals cache objects as JSON bytes.
type JSONByteStringCodec[V any] struct{}

var (
	_ CacheStorageCodec[any, []byte] = JSONByteStringCodec[any]{}
	_ BufferReleasePolicy            = JSONByteStringCodec[any]{}
	_ BufferEncoder[any]             = JSONByteStringCodec[any]{}
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
func (j JSONByteStringCodec[V]) Encode(value CacheObject[V]) ([]byte, error) {
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
func (j JSONByteStringCodec[V]) EncodeTo(buf *bytes.Buffer, value CacheObject[V]) error {
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
func (j JSONByteStringCodec[V]) Decode(data []byte) (CacheObject[V], error) {
	var out CacheObject[V]
	if err := json.Unmarshal(data, &out); err != nil {
		return CacheObject[V]{}, err
	}

	return out, nil
}

func (j JSONByteStringCodec[V]) CanReleaseBufferOnDecode() bool {
	return true
}

const (
	// DefaultCompressThresholdBytes is the default threshold size
	// above which values are compressed in BinaryCompressionCodec.
	DefaultCompressThresholdBytes = 1024 * 2 // 2 KiB

	CompressionTypeIDNone byte = 0x00
	CompressionTypeIDZlib byte = 0x01
)

var (
	ErrDecompressZeroLengthData     = errors.New("invalid data for decompression")
	ErrUnsupportedCompressionTypeID = errors.New("unsupported compression type ID")
)

type binaryCompressionCodec[V any] struct {
	inner                    CacheStorageCodec[V, []byte]
	innerBufferEncoder       BufferEncoder[V]
	compressThresholdBytes   int
	bufPool                  sync.Pool
	canReleaseBufferOnDecode bool
}

var _ CacheStorageCodec[any, []byte] = &binaryCompressionCodec[any]{}

// NewBinaryCompressionCodec returns a codec that conditionally compresses
// encoded values with zlib when they reach the threshold.
// A threshold of 0 always compresses, and a negative threshold disables compression.
func NewBinaryCompressionCodec[V any](
	inner CacheStorageCodec[V, []byte],
	compressThresholdBytes int,
) CacheStorageCodec[V, []byte] {
	canReleaseBufferOnDecode := false
	if policy, ok := any(inner).(BufferReleasePolicy); ok {
		canReleaseBufferOnDecode = policy.CanReleaseBufferOnDecode()
	}
	innerBufferEncoder, _ := any(inner).(BufferEncoder[V])

	return &binaryCompressionCodec[V]{
		inner:                  inner,
		innerBufferEncoder:     innerBufferEncoder,
		compressThresholdBytes: compressThresholdBytes,
		bufPool: sync.Pool{
			New: func() any {
				return bytes.NewBuffer(nil)
			},
		},
		canReleaseBufferOnDecode: canReleaseBufferOnDecode,
	}
}

func (b *binaryCompressionCodec[V]) Encode(value CacheObject[V]) ([]byte, error) {
	if b.innerBufferEncoder != nil {
		return b.encodeViaBuffer(value)
	}

	innerBuf, err := b.inner.Encode(value)
	if err != nil {
		return nil, err
	}
	if b.skipCompression(len(innerBuf)) {
		buf := make([]byte, 1+len(innerBuf))
		buf[0] = CompressionTypeIDNone
		copy(buf[1:], innerBuf)

		return buf, nil
	}

	return b.compress(innerBuf)
}

func (b *binaryCompressionCodec[V]) encodeViaBuffer(value CacheObject[V]) ([]byte, error) {
	innerBuf := b.acquireBuffer()
	defer b.returnBuffer(innerBuf)

	innerBuf.WriteByte(CompressionTypeIDNone)
	if err := b.innerBufferEncoder.EncodeTo(innerBuf, value); err != nil {
		return nil, err
	}
	encoded := innerBuf.Bytes()
	if b.skipCompression(len(encoded) - 1) {
		return bytes.Clone(encoded), nil
	}

	return b.compress(encoded[1:])
}

func (b *binaryCompressionCodec[V]) compress(data []byte) ([]byte, error) {
	compressBuf := b.acquireBuffer()
	defer b.returnBuffer(compressBuf)

	compressBuf.WriteByte(CompressionTypeIDZlib)
	if err := compressZlib(compressBuf, data); err != nil {
		return nil, err
	}

	return bytes.Clone(compressBuf.Bytes()), nil
}

func (b *binaryCompressionCodec[V]) skipCompression(innerLen int) bool {
	return b.compressThresholdBytes < 0 || innerLen < b.compressThresholdBytes
}

func (b *binaryCompressionCodec[V]) Decode(data []byte) (CacheObject[V], error) {
	if len(data) == 0 {
		return CacheObject[V]{}, ErrDecompressZeroLengthData
	}
	compressionTypeID := data[0]
	compressedData := data[1:]
	switch compressionTypeID {
	case CompressionTypeIDNone:
		return b.inner.Decode(compressedData)
	case CompressionTypeIDZlib:
		decompressBuf := b.acquireBuffer()
		if b.canReleaseBufferOnDecode {
			// decompressBuf MUST NOT be used outside of this function scope
			defer b.returnBuffer(decompressBuf)
		}

		err := decompressZlib(decompressBuf, compressedData)
		if err != nil {
			return CacheObject[V]{}, err
		}

		return b.inner.Decode(decompressBuf.Bytes())
	default:
		return CacheObject[V]{}, fmt.Errorf("unsupported compression type: %d", compressionTypeID)
	}
}

func (b *binaryCompressionCodec[V]) acquireBuffer() *bytes.Buffer {
	buf := b.bufPool.Get().(*bytes.Buffer)
	buf.Reset()

	return buf
}

func (b *binaryCompressionCodec[V]) returnBuffer(buf *bytes.Buffer) {
	buf.Reset()
	b.bufPool.Put(buf)
}

// zlibWriterPool avoids allocating a deflate state for every Encode call.
var zlibWriterPool = sync.Pool{
	New: func() any {
		return zlib.NewWriter(nil)
	},
}

func compressZlib(buf *bytes.Buffer, data []byte) error {
	writer := zlibWriterPool.Get().(*zlib.Writer)
	defer func() {
		// drop the reference to buf so that it can be released independently
		writer.Reset(nil)
		zlibWriterPool.Put(writer)
	}()

	writer.Reset(buf)
	if _, err := writer.Write(data); err != nil {
		_ = writer.Close()

		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	return nil
}

func decompressZlib(buf *bytes.Buffer, data []byte) error {
	reader, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer reader.Close()
	if _, err := buf.ReadFrom(reader); err != nil {
		return err
	}

	return nil
}

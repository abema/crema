package crema

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestJSONByteStringCodec_RoundTrip(t *testing.T) {
	t.Parallel()

	codec := JSONByteStringCodec[int]{}
	input := &CacheObject[int]{
		Value:          10,
		ExpireAtMillis: 1234,
	}
	encoded, err := codec.Encode(*input)
	if err != nil {
		t.Fatalf("expected encode to succeed, got %v", err)
	}
	if bytes.HasSuffix(encoded, []byte("\n")) {
		t.Fatalf("expected encoded JSON to not include trailing newline")
	}

	decoded, err := codec.Decode(encoded)
	if err != nil {
		t.Fatalf("expected decode to succeed, got %v", err)
	}
	if decoded != *input {
		t.Fatalf("expected decoded value %+v, got %+v", *input, decoded)
	}
}

func TestJSONByteStringCodec_DecodeError(t *testing.T) {
	t.Parallel()

	codec := JSONByteStringCodec[int]{}
	if _, err := codec.Decode([]byte("{")); err == nil {
		t.Fatal("expected decode error, got nil")
	}
}

func TestJSONByteStringCodec_EncodeError(t *testing.T) {
	t.Parallel()

	codec := JSONByteStringCodec[func()]{}
	input := &CacheObject[func()]{
		Value:          func() {},
		ExpireAtMillis: 1234,
	}
	_, err := codec.Encode(*input)
	if err == nil {
		t.Fatal("expected encode error, got nil")
	}
}

type binaryCompressionTestCodec struct{}

func (binaryCompressionTestCodec) Encode(value CacheObject[string]) ([]byte, error) {
	return []byte(value.Value + "|" + strconv.FormatInt(value.ExpireAtMillis, 10)), nil
}

func (binaryCompressionTestCodec) Decode(data []byte) (CacheObject[string], error) {
	parts := strings.SplitN(string(data), "|", 2)
	if len(parts) != 2 {
		return CacheObject[string]{}, errors.New("invalid payload")
	}
	expireAtMillis, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return CacheObject[string]{}, err
	}

	return CacheObject[string]{
		Value:          parts[0],
		ExpireAtMillis: expireAtMillis,
	}, nil
}

type emptyPayloadCodec struct{}

func (emptyPayloadCodec) Encode(value CacheObject[struct{}]) ([]byte, error) {
	return []byte{}, nil
}

func (emptyPayloadCodec) Decode(data []byte) (CacheObject[struct{}], error) {
	if len(data) != 0 {
		return CacheObject[struct{}]{}, errors.New("expected empty payload")
	}

	return CacheObject[struct{}]{}, nil
}

func TestBinaryCompressionCodec_RoundTripCompressed(t *testing.T) {
	t.Parallel()

	codec := NewBinaryCompressionCodec(binaryCompressionTestCodec{}, 0)
	input := CacheObject[string]{
		Value:          "hello",
		ExpireAtMillis: 1234,
	}
	encoded, err := codec.Encode(input)
	if err != nil {
		t.Fatalf("expected encode to succeed, got %v", err)
	}
	if encoded[0] != CompressionTypeIDZlib {
		t.Fatalf("expected zlib compression prefix, got %v", encoded[0])
	}

	decoded, err := codec.Decode(encoded)
	if err != nil {
		t.Fatalf("expected decode to succeed, got %v", err)
	}
	if decoded != input {
		t.Fatalf("expected decoded value %+v, got %+v", input, decoded)
	}
}

func TestBinaryCompressionCodec_RoundTripUncompressedUnderThreshold(t *testing.T) {
	t.Parallel()

	inner := binaryCompressionTestCodec{}
	input := CacheObject[string]{
		Value:          "hi",
		ExpireAtMillis: 5678,
	}
	innerBuf, err := inner.Encode(input)
	if err != nil {
		t.Fatalf("expected inner encode to succeed, got %v", err)
	}
	codec := NewBinaryCompressionCodec(inner, len(innerBuf)+1)

	encoded, err := codec.Encode(input)
	if err != nil {
		t.Fatalf("expected encode to succeed, got %v", err)
	}
	if encoded[0] != CompressionTypeIDNone {
		t.Fatalf("expected no compression prefix, got %v", encoded[0])
	}

	decoded, err := codec.Decode(encoded)
	if err != nil {
		t.Fatalf("expected decode to succeed, got %v", err)
	}
	if decoded != input {
		t.Fatalf("expected decoded value %+v, got %+v", input, decoded)
	}
}

func TestBinaryCompressionCodec_ZeroLengthInput(t *testing.T) {
	t.Parallel()

	codec := NewBinaryCompressionCodec(binaryCompressionTestCodec{}, 1)
	if _, err := codec.Decode(nil); !errors.Is(err, ErrDecompressZeroLengthData) {
		t.Fatalf("expected zero-length error, got %v", err)
	}
}

func TestBinaryCompressionCodec_ZeroLengthPayload(t *testing.T) {
	t.Parallel()

	codec := NewBinaryCompressionCodec(emptyPayloadCodec{}, 1)
	input := CacheObject[struct{}]{}
	encoded, err := codec.Encode(input)
	if err != nil {
		t.Fatalf("expected encode to succeed, got %v", err)
	}
	if len(encoded) != 1 || encoded[0] != CompressionTypeIDNone {
		t.Fatalf("expected no compression with empty payload, got %v", encoded)
	}

	decoded, err := codec.Decode(encoded)
	if err != nil {
		t.Fatalf("expected decode to succeed, got %v", err)
	}
	if decoded != input {
		t.Fatalf("expected decoded value %+v, got %+v", input, decoded)
	}
}

func TestBinaryCompressionCodec_DecodeCorruptedPayload(t *testing.T) {
	t.Parallel()

	codec := NewBinaryCompressionCodec(binaryCompressionTestCodec{}, 1)
	if _, err := codec.Decode([]byte{CompressionTypeIDZlib, 0x00, 0x01}); err == nil {
		t.Fatal("expected decode error for corrupted payload, got nil")
	}
}

func TestBinaryCompressionCodec_UnsupportedCompressionType(t *testing.T) {
	t.Parallel()

	codec := NewBinaryCompressionCodec(binaryCompressionTestCodec{}, 1)
	if _, err := codec.Decode([]byte{0xff, 0x00}); err == nil {
		t.Fatal("expected decode error for unsupported compression type, got nil")
	}
}

type bufferReleasePolicyCodec struct {
	binaryCompressionTestCodec

	canRelease bool
}

func (b bufferReleasePolicyCodec) CanReleaseBufferOnDecode() bool {
	return b.canRelease
}

func TestBinaryCompressionCodec_CanReleaseBufferOnDecode(t *testing.T) {
	t.Parallel()

	codec := NewBinaryCompressionCodec(binaryCompressionTestCodec{}, 1)
	binaryCodec, ok := any(codec).(*binaryCompressionCodec[string])
	if !ok {
		t.Fatalf("expected binary compression codec, got %T", codec)
	}
	if binaryCodec.canReleaseBufferOnDecode {
		t.Fatal("expected canReleaseBufferOnDecode to be false by default")
	}

	withPolicy := NewBinaryCompressionCodec(bufferReleasePolicyCodec{canRelease: true}, 1)
	binaryWithPolicy, ok := any(withPolicy).(*binaryCompressionCodec[string])
	if !ok {
		t.Fatalf("expected binary compression codec, got %T", withPolicy)
	}
	if !binaryWithPolicy.canReleaseBufferOnDecode {
		t.Fatal("expected canReleaseBufferOnDecode to be true with policy")
	}
}

type bufferCheckingCodec struct {
	buf            *bytes.Buffer
	sawSameBacking bool
}

func (b *bufferCheckingCodec) Encode(value CacheObject[[]byte]) ([]byte, error) {
	return value.Value, nil
}

func (b *bufferCheckingCodec) Decode(data []byte) (CacheObject[[]byte], error) {
	if len(data) == 0 || len(b.buf.Bytes()) == 0 {
		return CacheObject[[]byte]{}, errors.New("empty payload")
	}
	if &data[0] == &b.buf.Bytes()[0] {
		b.sawSameBacking = true
	}

	return CacheObject[[]byte]{Value: append([]byte(nil), data...)}, nil
}

func (b *bufferCheckingCodec) CanReleaseBufferOnDecode() bool {
	return true
}

func TestBinaryCompressionCodec_CanReleaseBufferOnDecodeTrueUsesBuffer(t *testing.T) {
	t.Parallel()

	pooled := bytes.NewBuffer(nil)
	inner := &bufferCheckingCodec{buf: pooled}
	codec := &binaryCompressionCodec[[]byte]{
		inner:                  inner,
		compressThresholdBytes: 0,
		bufPool: sync.Pool{
			New: func() any {
				return pooled
			},
		},
		canReleaseBufferOnDecode: true,
	}

	compressBuf := bytes.NewBuffer(nil)
	if err := compressZlib(compressBuf, []byte("hello")); err != nil {
		t.Fatalf("compressZlib() error = %v", err)
	}
	data := append([]byte{CompressionTypeIDZlib}, compressBuf.Bytes()...)

	if _, err := codec.Decode(data); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !inner.sawSameBacking {
		t.Fatal("expected decode to pass pooled buffer to inner codec")
	}
}

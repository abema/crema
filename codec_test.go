package crema

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
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

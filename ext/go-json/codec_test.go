package gojson

import (
	"bytes"
	"testing"

	"github.com/abema/crema"
)

func TestJSONByteStringCodec_RoundTrip(t *testing.T) {
	t.Parallel()

	codec := JSONByteStringCodec[int]{}
	input := &crema.CacheObject[int]{
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
	input := &crema.CacheObject[func()]{
		Value:          func() {},
		ExpireAtMillis: 1234,
	}
	_, err := codec.Encode(*input)
	if err == nil {
		t.Fatal("expected encode error, got nil")
	}
}

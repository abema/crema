package gojson

import (
	"bytes"
	"strconv"
	"strings"
	"sync"
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

func TestJSONByteStringCodec_EncodeToAppendsWithoutTrailingNewline(t *testing.T) {
	t.Parallel()

	codec := JSONByteStringCodec[int]{}
	input := crema.CacheObject[int]{
		Value:          10,
		ExpireAtMillis: 1234,
	}
	buf := bytes.NewBufferString("prefix")
	if err := codec.EncodeTo(buf, input); err != nil {
		t.Fatalf("EncodeTo() error = %v", err)
	}

	encoded, err := codec.Encode(input)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if got, want := buf.String(), "prefix"+string(encoded); got != want {
		t.Fatalf("expected EncodeTo to append %q, got %q", want, got)
	}
}

func TestJSONByteStringCodec_EncodeToErrorRestoresBuffer(t *testing.T) {
	t.Parallel()

	codec := JSONByteStringCodec[func()]{}
	buf := bytes.NewBufferString("prefix")
	if err := codec.EncodeTo(buf, crema.CacheObject[func()]{Value: func() {}}); err == nil {
		t.Fatal("expected EncodeTo error, got nil")
	}
	if got := buf.String(); got != "prefix" {
		t.Fatalf("expected buffer to be restored to %q, got %q", "prefix", got)
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

func TestJSONByteStringCodec_EncodeConcurrent(t *testing.T) {
	t.Parallel()

	codec := JSONByteStringCodec[string]{}
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			value := strings.Repeat(strconv.Itoa(i%10), i)
			want := `{"Value":"` + value + `","ExpireAtMillis":` + strconv.Itoa(i) + `}`
			for j := 0; j < 100; j++ {
				encoded, err := codec.Encode(crema.CacheObject[string]{
					Value:          value,
					ExpireAtMillis: int64(i),
				})
				if err != nil {
					t.Errorf("expected encode to succeed, got %v", err)

					return
				}
				if string(encoded) != want {
					t.Errorf("expected encoded %s, got %s", want, encoded)

					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestJSONByteStringCodec_EncodeResultIsNotAliasedByLaterEncode(t *testing.T) {
	t.Parallel()

	codec := JSONByteStringCodec[string]{}
	first, err := codec.Encode(crema.CacheObject[string]{Value: "aaaa", ExpireAtMillis: 1})
	if err != nil {
		t.Fatalf("expected encode to succeed, got %v", err)
	}
	snapshot := string(first)

	if _, err := codec.Encode(crema.CacheObject[string]{Value: "bbbb", ExpireAtMillis: 2}); err != nil {
		t.Fatalf("expected encode to succeed, got %v", err)
	}
	if string(first) != snapshot {
		t.Fatalf("expected first result to stay %s, got %s", snapshot, first)
	}
}

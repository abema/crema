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

func TestNoopCacheStorageCodec_RoundTrip(t *testing.T) {
	t.Parallel()

	codec := NoopCacheStorageCodec[string]{}
	input := CacheObject[string]{
		Value:          "hello",
		ExpireAtMillis: 1234,
	}

	encoded, err := codec.Encode(input)
	if err != nil {
		t.Fatalf("expected encode to succeed, got %v", err)
	}
	if encoded != input {
		t.Fatalf("expected encoded value %+v, got %+v", input, encoded)
	}

	decoded, err := codec.Decode(encoded)
	if err != nil {
		t.Fatalf("expected decode to succeed, got %v", err)
	}
	if decoded != input {
		t.Fatalf("expected decoded value %+v, got %+v", input, decoded)
	}
}

func TestJSONByteStringCodec_DoesNotEscapeHTML(t *testing.T) {
	t.Parallel()

	codec := JSONByteStringCodec[string]{}
	encoded, err := codec.Encode(CacheObject[string]{
		Value:          "<tag>&",
		ExpireAtMillis: 1234,
	})
	if err != nil {
		t.Fatalf("expected encode to succeed, got %v", err)
	}
	if !bytes.Contains(encoded, []byte(`"<tag>&"`)) {
		t.Fatalf("expected encoded JSON to preserve HTML characters, got %s", encoded)
	}
	if bytes.Contains(encoded, []byte(`\u003c`)) || bytes.Contains(encoded, []byte(`\u003e`)) || bytes.Contains(encoded, []byte(`\u0026`)) {
		t.Fatalf("expected encoded JSON to not escape HTML characters, got %s", encoded)
	}
}

func TestJSONByteStringCodec_EncodeToAppendsWithoutTrailingNewline(t *testing.T) {
	t.Parallel()

	codec := JSONByteStringCodec[int]{}
	input := CacheObject[int]{
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

	decoded, err := codec.Decode(buf.Bytes()[len("prefix"):])
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if decoded != input {
		t.Fatalf("expected decoded value %+v, got %+v", input, decoded)
	}
}

func TestJSONByteStringCodec_EncodeToErrorRestoresBuffer(t *testing.T) {
	t.Parallel()

	codec := JSONByteStringCodec[func()]{}
	buf := bytes.NewBufferString("prefix")
	if err := codec.EncodeTo(buf, CacheObject[func()]{Value: func() {}}); err == nil {
		t.Fatal("expected EncodeTo error, got nil")
	}
	if got := buf.String(); got != "prefix" {
		t.Fatalf("expected buffer to be restored to %q, got %q", "prefix", got)
	}
}

func TestJSONByteStringCodec_CanReleaseBufferOnDecode(t *testing.T) {
	t.Parallel()

	codec := JSONByteStringCodec[int]{}
	if !codec.CanReleaseBufferOnDecode() {
		t.Fatal("expected JSON codec to allow buffer release on decode")
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
				encoded, err := codec.Encode(CacheObject[string]{
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
	first, err := codec.Encode(CacheObject[string]{Value: "aaaa", ExpireAtMillis: 1})
	if err != nil {
		t.Fatalf("expected encode to succeed, got %v", err)
	}
	snapshot := string(first)

	if _, err := codec.Encode(CacheObject[string]{Value: "bbbb", ExpireAtMillis: 2}); err != nil {
		t.Fatalf("expected encode to succeed, got %v", err)
	}
	if string(first) != snapshot {
		t.Fatalf("expected first result to stay %s, got %s", snapshot, first)
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

var errEncodeFailed = errors.New("encode failed")

type encodeErrorCodec struct{}

func (encodeErrorCodec) Encode(value CacheObject[string]) ([]byte, error) {
	return nil, errEncodeFailed
}

func (encodeErrorCodec) Decode(data []byte) (CacheObject[string], error) {
	return CacheObject[string]{}, nil
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

func TestBinaryCompressionCodec_RoundTripCompressedAtThreshold(t *testing.T) {
	t.Parallel()

	inner := binaryCompressionTestCodec{}
	input := CacheObject[string]{
		Value:          "hello",
		ExpireAtMillis: 1234,
	}
	innerBuf, err := inner.Encode(input)
	if err != nil {
		t.Fatalf("expected inner encode to succeed, got %v", err)
	}
	codec := NewBinaryCompressionCodec(inner, len(innerBuf))

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

func TestBinaryCompressionCodec_RoundTripUncompressedWithNegativeThreshold(t *testing.T) {
	t.Parallel()

	codec := NewBinaryCompressionCodec(binaryCompressionTestCodec{}, -1)
	input := CacheObject[string]{
		Value:          strings.Repeat("a", DefaultCompressThresholdBytes),
		ExpireAtMillis: 5678,
	}

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

func TestBinaryCompressionCodec_EncodeError(t *testing.T) {
	t.Parallel()

	codec := NewBinaryCompressionCodec(encodeErrorCodec{}, 0)
	_, err := codec.Encode(CacheObject[string]{Value: "hello"})
	if !errors.Is(err, errEncodeFailed) {
		t.Fatalf("expected encode error, got %v", err)
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

func TestBinaryCompressionCodec_DecodeTruncatedPayload(t *testing.T) {
	t.Parallel()

	compressBuf := bytes.NewBuffer(nil)
	if err := compressZlib(compressBuf, []byte("hello")); err != nil {
		t.Fatalf("compressZlib() error = %v", err)
	}
	compressed := compressBuf.Bytes()
	if len(compressed) < 2 {
		t.Fatalf("expected compressed data to include zlib payload, got %v", compressed)
	}

	codec := NewBinaryCompressionCodec(binaryCompressionTestCodec{}, 1)
	data := append([]byte{CompressionTypeIDZlib}, compressed[:len(compressed)-1]...)
	if _, err := codec.Decode(data); err == nil {
		t.Fatal("expected decode error for truncated payload, got nil")
	}
}

func TestBinaryCompressionCodec_UnsupportedCompressionType(t *testing.T) {
	t.Parallel()

	codec := NewBinaryCompressionCodec(binaryCompressionTestCodec{}, 1)
	if _, err := codec.Decode([]byte{0xff, 0x00}); err == nil {
		t.Fatal("expected decode error for unsupported compression type, got nil")
	}
}

// bufferEncoderCodec encodes only through EncodeTo so that tests can detect
// whether binaryCompressionCodec took the BufferEncoder path.
type bufferEncoderCodec struct {
	binaryCompressionTestCodec
}

func (bufferEncoderCodec) Encode(value CacheObject[string]) ([]byte, error) {
	return nil, errEncodeFailed
}

func (c bufferEncoderCodec) EncodeTo(buf *bytes.Buffer, value CacheObject[string]) error {
	encoded, err := c.binaryCompressionTestCodec.Encode(value)
	if err != nil {
		return err
	}
	buf.Write(encoded)

	return nil
}

func TestBinaryCompressionCodec_BufferEncoderRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		threshold         int
		value             string
		compressionTypeID byte
	}{
		{
			name:              "compressed",
			threshold:         0,
			value:             "hello",
			compressionTypeID: CompressionTypeIDZlib,
		},
		{
			name:              "uncompressed",
			threshold:         DefaultCompressThresholdBytes,
			value:             "hello",
			compressionTypeID: CompressionTypeIDNone,
		},
		{
			name:              "uncompressed with negative threshold",
			threshold:         -1,
			value:             strings.Repeat("a", DefaultCompressThresholdBytes),
			compressionTypeID: CompressionTypeIDNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			input := CacheObject[string]{
				Value:          tt.value,
				ExpireAtMillis: 1234,
			}
			codec := NewBinaryCompressionCodec(bufferEncoderCodec{}, tt.threshold)
			encoded, err := codec.Encode(input)
			if err != nil {
				t.Fatalf("Encode() error = %v", err)
			}
			if encoded[0] != tt.compressionTypeID {
				t.Fatalf("expected compression type ID %v, got %v", tt.compressionTypeID, encoded[0])
			}

			plain, err := NewBinaryCompressionCodec(binaryCompressionTestCodec{}, tt.threshold).Encode(input)
			if err != nil {
				t.Fatalf("Encode() error = %v", err)
			}
			if !bytes.Equal(encoded, plain) {
				t.Fatalf("expected identical encoded bytes, got %v and %v", encoded, plain)
			}

			decoded, err := codec.Decode(encoded)
			if err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			if decoded != input {
				t.Fatalf("expected decoded value %+v, got %+v", input, decoded)
			}
		})
	}
}

func TestBinaryCompressionCodec_BufferEncoderEncodeError(t *testing.T) {
	t.Parallel()

	codec := NewBinaryCompressionCodec[func()](JSONByteStringCodec[func()]{}, 0)
	if _, err := codec.Encode(CacheObject[func()]{Value: func() {}}); err == nil {
		t.Fatal("expected encode error, got nil")
	}
}

func TestBinaryCompressionCodec_BufferEncoderRepeatedEncode(t *testing.T) {
	t.Parallel()

	codec := NewBinaryCompressionCodec(bufferEncoderCodec{}, DefaultCompressThresholdBytes)
	binaryCodec, ok := any(codec).(*binaryCompressionCodec[string])
	if !ok {
		t.Fatalf("expected binary compression codec, got %T", codec)
	}
	if binaryCodec.innerBufferEncoder == nil {
		t.Fatal("expected inner buffer encoder to be detected")
	}

	for _, value := range []string{"hello", "hi", strings.Repeat("a", 128)} {
		input := CacheObject[string]{Value: value, ExpireAtMillis: 1234}
		encoded, err := codec.Encode(input)
		if err != nil {
			t.Fatalf("Encode() error = %v", err)
		}
		decoded, err := codec.Decode(encoded)
		if err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if decoded != input {
			t.Fatalf("expected decoded value %+v, got %+v", input, decoded)
		}
	}
}

func TestBinaryCompressionCodec_ConcurrentEncode(t *testing.T) {
	t.Parallel()

	codec := NewBinaryCompressionCodec(binaryCompressionTestCodec{}, 0)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()

			input := CacheObject[string]{
				Value:          strings.Repeat(strconv.Itoa(i), 1+i*8),
				ExpireAtMillis: int64(i),
			}
			for range 16 {
				encoded, err := codec.Encode(input)
				if err != nil {
					t.Errorf("Encode() error = %v", err)

					return
				}
				decoded, err := codec.Decode(encoded)
				if err != nil {
					t.Errorf("Decode() error = %v", err)

					return
				}
				if decoded != input {
					t.Errorf("expected decoded value %+v, got %+v", input, decoded)

					return
				}
			}
		}()
	}
	wg.Wait()
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

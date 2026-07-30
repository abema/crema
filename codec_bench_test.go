package crema

import (
	"strconv"
	"strings"
	"testing"
)

type jsonBenchmarkPayload struct {
	ID      string
	Count   int
	Enabled bool
	Values  []int
	Meta    map[string]string
}

func newJSONBenchmarkCacheObject(valueSize int) CacheObject[jsonBenchmarkPayload] {
	values := make([]int, valueSize)
	meta := make(map[string]string, valueSize)
	for i := range values {
		values[i] = i
		meta["key"+strconv.Itoa(i)] = "value" + strconv.Itoa(i)
	}

	return CacheObject[jsonBenchmarkPayload]{
		Value: jsonBenchmarkPayload{
			ID:      "bench",
			Count:   42,
			Enabled: true,
			Values:  values,
			Meta:    meta,
		},
		ExpireAtMillis: 1234,
	}
}

func benchmarkBinaryCompressionInput(size int) CacheObject[string] {
	return CacheObject[string]{
		Value:          strings.Repeat("crema cache payload ", 1+size/20)[:size],
		ExpireAtMillis: 1234,
	}
}

func BenchmarkJSONByteStringCodecEncode(b *testing.B) {
	codec := JSONByteStringCodec[jsonBenchmarkPayload]{}
	for _, valueSize := range []int{1, 16, 256} {
		input := newJSONBenchmarkCacheObject(valueSize)
		b.Run("size="+strconv.Itoa(valueSize), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := codec.Encode(input); err != nil {
					b.Fatalf("Encode() error = %v", err)
				}
			}
		})
	}
}

func BenchmarkJSONByteStringCodecEncodeParallel(b *testing.B) {
	codec := JSONByteStringCodec[jsonBenchmarkPayload]{}
	input := newJSONBenchmarkCacheObject(16)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := codec.Encode(input); err != nil {
				b.Fatalf("Encode() error = %v", err)
			}
		}
	})
}

func BenchmarkBinaryCompressionCodecEncode(b *testing.B) {
	benchmarks := []struct {
		name  string
		codec CacheStorageCodec[string, []byte]
		input CacheObject[string]
	}{
		{
			name:  "uncompressed",
			codec: NewBinaryCompressionCodec[string](JSONByteStringCodec[string]{}, DefaultCompressThresholdBytes),
			input: benchmarkBinaryCompressionInput(256),
		},
		{
			name:  "compressed",
			codec: NewBinaryCompressionCodec[string](JSONByteStringCodec[string]{}, DefaultCompressThresholdBytes),
			input: benchmarkBinaryCompressionInput(8 * 1024),
		},
		{
			name:  "uncompressed_inner_without_buffer_encoder",
			codec: NewBinaryCompressionCodec[string](binaryCompressionTestCodec{}, DefaultCompressThresholdBytes),
			input: benchmarkBinaryCompressionInput(256),
		},
		{
			name:  "compressed_inner_without_buffer_encoder",
			codec: NewBinaryCompressionCodec[string](binaryCompressionTestCodec{}, DefaultCompressThresholdBytes),
			input: benchmarkBinaryCompressionInput(8 * 1024),
		},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := bm.codec.Encode(bm.input); err != nil {
					b.Fatalf("Encode() error = %v", err)
				}
			}
		})
	}
}

package crema

import (
	"strings"
	"testing"
)

func benchmarkBinaryCompressionInput(size int) CacheObject[string] {
	return CacheObject[string]{
		Value:          strings.Repeat("crema cache payload ", 1+size/20)[:size],
		ExpireAtMillis: 1234,
	}
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

package gojson

import (
	"testing"

	"github.com/abema/crema"
)

type benchPayload struct {
	ID      string
	Count   int
	Enabled bool
	Values  []int
	Meta    map[string]string
}

func BenchmarkJSONByteStringCodecEncode(b *testing.B) {
	std := crema.JSONByteStringCodec[benchPayload]{}
	fast := JSONByteStringCodec[benchPayload]{}
	input := &crema.CacheObject[benchPayload]{
		Value: benchPayload{
			ID:      "bench",
			Count:   42,
			Enabled: true,
			Values:  []int{1, 2, 3, 4, 5},
			Meta: map[string]string{
				"env":  "test",
				"role": "benchmark",
			},
		},
		ExpireAtMillis: 1234,
	}

	b.Run("std", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := std.Encode(*input); err != nil {
				b.Fatalf("encode failed: %v", err)
			}
		}
	})

	b.Run("go-json", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := fast.Encode(*input); err != nil {
				b.Fatalf("encode failed: %v", err)
			}
		}
	})
}

func BenchmarkJSONByteStringCodecDecode(b *testing.B) {
	std := crema.JSONByteStringCodec[benchPayload]{}
	fast := JSONByteStringCodec[benchPayload]{}
	input := &crema.CacheObject[benchPayload]{
		Value: benchPayload{
			ID:      "bench",
			Count:   42,
			Enabled: true,
			Values:  []int{1, 2, 3, 4, 5},
			Meta: map[string]string{
				"env":  "test",
				"role": "benchmark",
			},
		},
		ExpireAtMillis: 1234,
	}

	encoded, err := std.Encode(*input)
	if err != nil {
		b.Fatalf("encode failed: %v", err)
	}

	b.Run("std", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := std.Decode(encoded); err != nil {
				b.Fatalf("decode failed: %v", err)
			}
		}
	})

	b.Run("go-json", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := fast.Decode(encoded); err != nil {
				b.Fatalf("decode failed: %v", err)
			}
		}
	})
}

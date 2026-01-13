package protobuf

import (
	"testing"

	"github.com/abema/crema"
	testproto "github.com/abema/crema/ext/protobuf/internal/proto"
)

func BenchmarkProtobufCodecEncode(b *testing.B) {
	codec, err := NewProtobufCodec(&testproto.ProtoTestObject{})
	if err != nil {
		b.Fatalf("NewProtobufCodec() error = %v", err)
	}
	value := &testproto.ProtoTestObject{}
	value.SetValue(123)
	input := crema.CacheObject[*testproto.ProtoTestObject]{
		Value:          value,
		ExpireAtMillis: 456,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := codec.Encode(input); err != nil {
			b.Fatalf("Encode() error = %v", err)
		}
	}
}

func BenchmarkProtobufCodecDecode(b *testing.B) {
	codec, err := NewProtobufCodec(&testproto.ProtoTestObject{})
	if err != nil {
		b.Fatalf("NewProtobufCodec() error = %v", err)
	}
	value := &testproto.ProtoTestObject{}
	value.SetValue(123)
	input := crema.CacheObject[*testproto.ProtoTestObject]{
		Value:          value,
		ExpireAtMillis: 456,
	}
	encoded, err := codec.Encode(input)
	if err != nil {
		b.Fatalf("Encode() error = %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		decoded, err := codec.Decode(encoded)
		if err != nil {
			b.Fatalf("Decode() error = %v", err)
		}
		if got := decoded.Value.GetValue(); got != 123 {
			b.Fatalf("decoded value = %d, want %d", got, 123)
		}
	}
}

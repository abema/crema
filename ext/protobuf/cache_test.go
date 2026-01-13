package protobuf

import (
	"testing"

	"github.com/abema/crema"
	testproto "github.com/abema/crema/ext/protobuf/internal/proto"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestProtobufCodec_EncodeDecodeRoundTrip(t *testing.T) {
	t.Parallel()

	codec, err := NewProtobufCodec(&testproto.ProtoTestObject{})
	if err != nil {
		t.Fatalf("NewProtobufCodec() error = %v", err)
	}
	value := &testproto.ProtoTestObject{}
	value.SetValue(123)

	in := crema.CacheObject[*testproto.ProtoTestObject]{
		Value:          value,
		ExpireAtMillis: 456,
	}

	encoded, err := codec.Encode(in)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	out, err := codec.Decode(encoded)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}

	if got := out.Value.GetValue(); got != 123 {
		t.Fatalf("decoded value = %d, want %d", got, 123)
	}
	if out.ExpireAtMillis != 456 {
		t.Fatalf("decoded expiration = %d, want %d", out.ExpireAtMillis, 456)
	}
}

func TestNewProtobufCodec_RejectsNilPrototype(t *testing.T) {
	t.Parallel()

	_, err := NewProtobufCodec[*testproto.ProtoTestObject](nil)
	if err == nil {
		t.Fatal("NewProtobufCodec() error = nil, want error")
	}
}

func TestNewProtobufCodec_RejectsTypedNilPrototype(t *testing.T) {
	t.Parallel()

	var prototype *testproto.ProtoTestObject
	_, err := NewProtobufCodec(prototype)
	if err == nil {
		t.Fatal("NewProtobufCodec() error = nil, want error")
	}
}

func TestProtobufCodec_DecodeUsesPrototypeWhenValueNil(t *testing.T) {
	t.Parallel()

	codec, err := NewProtobufCodec(&testproto.ProtoTestObject{})
	if err != nil {
		t.Fatalf("NewProtobufCodec() error = %v", err)
	}

	in := crema.CacheObject[*testproto.ProtoTestObject]{
		Value:          &testproto.ProtoTestObject{},
		ExpireAtMillis: 123,
	}
	in.Value.SetValue(987)

	encoded, err := codec.Encode(in)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	out, err := codec.Decode(encoded)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}

	if out.Value == nil {
		t.Fatal("decoded value is nil, want non-nil")
	}
	if got := out.Value.GetValue(); got != 987 {
		t.Fatalf("decoded value = %d, want %d", got, 987)
	}
}

func TestProtobufCodec_DecodeReturnsValue(t *testing.T) {
	t.Parallel()

	codec, err := NewProtobufCodec(&testproto.ProtoTestObject{})
	if err != nil {
		t.Fatalf("NewProtobufCodec() error = %v", err)
	}

	in := crema.CacheObject[*testproto.ProtoTestObject]{
		Value:          &testproto.ProtoTestObject{},
		ExpireAtMillis: 321,
	}
	in.Value.SetValue(555)

	encoded, err := codec.Encode(in)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	out, err := codec.Decode(encoded)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if out.Value == nil {
		t.Fatal("decoded value is nil, want non-nil")
	}
	if got := out.Value.GetValue(); got != 555 {
		t.Fatalf("decoded value = %d, want %d", got, 555)
	}
}

func TestProtobufCodec_DecodeRejectsInvalidData(t *testing.T) {
	t.Parallel()

	codec, err := NewProtobufCodec(&testproto.ProtoTestObject{})
	if err != nil {
		t.Fatalf("NewProtobufCodec() error = %v", err)
	}

	if _, err := codec.Decode([]byte("not-a-proto")); err == nil {
		t.Fatal("Decode() error = nil, want error")
	}
}

func TestProtobufCodec_DecodeRejectsInvalidSerializedValue(t *testing.T) {
	t.Parallel()

	codec, err := NewProtobufCodec(&testproto.ProtoTestObject{})
	if err != nil {
		t.Fatalf("NewProtobufCodec() error = %v", err)
	}

	envelope := &testproto.ProtoCacheObject{}
	envelope.SetSerializedValue([]byte{0xff})
	encoded, err := proto.Marshal(envelope)
	if err != nil {
		t.Fatalf("proto.Marshal() error = %v", err)
	}

	if _, err := codec.Decode(encoded); err == nil {
		t.Fatal("Decode() error = nil, want error")
	}
}

func TestProtobufCodec_EncodeRejectsInvalidUTF8(t *testing.T) {
	t.Parallel()

	codec, err := NewProtobufCodec(&structpb.Struct{})
	if err != nil {
		t.Fatalf("NewProtobufCodec() error = %v", err)
	}

	in := crema.CacheObject[*structpb.Struct]{
		Value: &structpb.Struct{
			Fields: map[string]*structpb.Value{
				string([]byte{0xff}): structpb.NewStringValue("ok"),
			},
		},
	}

	if _, err := codec.Encode(in); err == nil {
		t.Fatal("Encode() error = nil, want error")
	}
}

type valueProtoMessage struct{}

func (valueProtoMessage) ProtoReflect() protoreflect.Message {
	return nil
}

func TestIsNilPrototype_NilInterface(t *testing.T) {
	t.Parallel()

	var prototype proto.Message
	if got := isNilPrototype(prototype); !got {
		t.Fatal("isNilPrototype() = false, want true")
	}
}

func TestIsNilPrototype_NonNilValue(t *testing.T) {
	t.Parallel()

	prototype := valueProtoMessage{}
	if got := isNilPrototype(prototype); got {
		t.Fatal("isNilPrototype() = true, want false")
	}
}

package protobuf

import (
	"errors"
	"reflect"

	"github.com/abema/crema"
	internalproto "github.com/abema/crema/ext/protobuf/internal/proto"
	"google.golang.org/protobuf/proto"
)

const protoCacheEnvelopeVersion = 1

// ErrCacheObjectEnvelopeVersionMismatch is returned when the envelope version is unsupported.
var ErrCacheObjectEnvelopeVersionMismatch = errors.New("protobuf cache object version mismatch")

// ErrNilPrototype is returned when a nil prototype is provided.
var ErrNilPrototype = errors.New("protobuf codec requires Prototype to construct messages")

// ProtobufCodec encodes/decodes crema.CacheObject values using protobuf.
type ProtobufCodec[V proto.Message] struct {
	Prototype V
}

var _ crema.SerializationCodec[proto.Message, []byte] = ProtobufCodec[proto.Message]{}

var marshalOptions = proto.MarshalOptions{}
var unmarshalOptions = proto.UnmarshalOptions{}

// NewProtobufCodec creates a codec with a non-nil prototype message.
// Pass a zero-value instance of the concrete protobuf message you will cache,
// e.g. &mypb.MyMessage{}; it is used only for allocating new messages on decode.
func NewProtobufCodec[V proto.Message](prototype V) (ProtobufCodec[V], error) {
	if isNilPrototype(prototype) {
		return ProtobufCodec[V]{}, ErrNilPrototype
	}
	return ProtobufCodec[V]{Prototype: prototype}, nil
}

// Encode marshals a cache object into the protobuf envelope format.
func (p ProtobufCodec[V]) Encode(value crema.CacheObject[V]) ([]byte, error) {
	serializedValue, err := proto.Marshal(value.Value)
	if err != nil {
		return nil, err
	}
	envelope := &internalproto.ProtoCacheObject{}
	envelope.SetVersion(protoCacheEnvelopeVersion)
	envelope.SetSerializedValue(serializedValue)
	envelope.SetExpireAtMillis(value.ExpireAtMillis)
	encoded, err := marshalOptions.MarshalAppend(nil, envelope)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// Decode unmarshals the protobuf envelope into a cache object.
func (p ProtobufCodec[V]) Decode(data []byte) (crema.CacheObject[V], error) {
	var envelope internalproto.ProtoCacheObject
	if err := unmarshalOptions.Unmarshal(data, &envelope); err != nil {
		return crema.CacheObject[V]{}, err
	}

	msg := p.Prototype.ProtoReflect().New().Interface().(V)
	if err := unmarshalOptions.Unmarshal(envelope.GetSerializedValue(), msg); err != nil {
		return crema.CacheObject[V]{}, err
	}
	return crema.CacheObject[V]{
		Value:          msg,
		ExpireAtMillis: envelope.GetExpireAtMillis(),
	}, nil
}

func isNilPrototype[V proto.Message](prototype V) bool {
	if any(prototype) == nil {
		return true
	}
	value := reflect.ValueOf(prototype)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

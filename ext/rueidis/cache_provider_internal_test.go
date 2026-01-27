package rueidis

import (
	"errors"
	"testing"
	"unsafe"

	"github.com/redis/rueidis"
)

const (
	respTypeInteger = byte(':')
	respTypeNull    = byte('_')
)

type rawRedisMessage struct {
	attrs  *rueidis.RedisMessage
	bytes  *byte
	array  *rueidis.RedisMessage
	intlen int64
	typ    byte
	ttl    [7]byte
}

func TestParseRedisGetMessage_Error(t *testing.T) {
	t.Parallel()

	expected := errors.New("boom")

	_, ok, err := parseRedisGetMessage(rueidis.RedisMessage{}, expected)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, expected) {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected ok to be false")
	}
}

func TestParseRedisGetMessage_RedisNilError(t *testing.T) {
	t.Parallel()

	value, ok, err := parseRedisGetMessage(rueidis.RedisMessage{}, rueidis.Nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected ok to be false")
	}
	if value != nil {
		t.Fatal("expected value to be nil")
	}
}

func TestParseRedisGetMessage_NilMessage(t *testing.T) {
	t.Parallel()

	msg := newRedisNullMessage()

	value, ok, err := parseRedisGetMessage(msg, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected ok to be false")
	}
	if value != nil {
		t.Fatal("expected value to be nil")
	}
}

func TestParseRedisGetMessage_AsBytesError(t *testing.T) {
	t.Parallel()

	msg := newRedisIntMessage(1)

	value, ok, err := parseRedisGetMessage(msg, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if ok {
		t.Fatal("expected ok to be false")
	}
	if value != nil {
		t.Fatal("expected value to be nil")
	}
}

func newRedisNullMessage() rueidis.RedisMessage {
	var msg rueidis.RedisMessage
	raw := (*rawRedisMessage)(unsafe.Pointer(&msg))
	raw.typ = respTypeNull

	return msg
}

func newRedisIntMessage(value int64) rueidis.RedisMessage {
	var msg rueidis.RedisMessage
	raw := (*rawRedisMessage)(unsafe.Pointer(&msg))
	raw.typ = respTypeInteger
	raw.intlen = value

	return msg
}

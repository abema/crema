package valkeygo

import (
	"errors"
	"testing"
	"unsafe"

	"github.com/valkey-io/valkey-go"
)

const (
	respTypeInteger = byte(':')
	respTypeNull    = byte('_')
)

type rawValkeyMessage struct {
	attrs  *valkey.ValkeyMessage
	bytes  *byte
	array  *valkey.ValkeyMessage
	intlen int64
	typ    byte
	ttl    [7]byte
}

func TestParseValkeyGetMessage_Error(t *testing.T) {
	t.Parallel()

	expected := errors.New("boom")

	_, ok, err := parseValkeyGetMessage(valkey.ValkeyMessage{}, expected)
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

func TestParseValkeyGetMessage_ValkeyNilError(t *testing.T) {
	t.Parallel()

	value, ok, err := parseValkeyGetMessage(valkey.ValkeyMessage{}, valkey.Nil)
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

func TestParseValkeyGetMessage_NilMessage(t *testing.T) {
	t.Parallel()

	msg := newValkeyNullMessage()

	value, ok, err := parseValkeyGetMessage(msg, nil)
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

func TestParseValkeyGetMessage_AsBytesError(t *testing.T) {
	t.Parallel()

	msg := newValkeyIntMessage(1)

	value, ok, err := parseValkeyGetMessage(msg, nil)
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

func newValkeyNullMessage() valkey.ValkeyMessage {
	var msg valkey.ValkeyMessage
	raw := (*rawValkeyMessage)(unsafe.Pointer(&msg))
	raw.typ = respTypeNull

	return msg
}

func newValkeyIntMessage(value int64) valkey.ValkeyMessage {
	var msg valkey.ValkeyMessage
	raw := (*rawValkeyMessage)(unsafe.Pointer(&msg))
	raw.typ = respTypeInteger
	raw.intlen = value

	return msg
}

package database

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
)

func TestRedisWireRejectsUnboundedAndMalformedReplies(t *testing.T) {
	for _, raw := range []string{"$67108865\r\n", "*16385\r\n", "$-2\r\n", "$2\r\nx\r\n", ":oops\r\n", "+" + strings.Repeat("a", 5000) + "\r\n", "*1\r\n*1\r\n*1\r\n*1\r\n*1\r\n*1\r\n+OK\r\n", "-ERR secret fixture body\r\n"} {
		budget := RedisCopyMaxValue + (2 << 20)
		_, err := readRedisReply(bufio.NewReader(strings.NewReader(raw)), 0, &budget)
		if err == nil || strings.Contains(err.Error(), "secret fixture") {
			t.Fatalf("malformed reply accepted or disclosed: %v", err)
		}
	}
}

func TestRedisWirePreservesBinaryKeysAndValues(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	conn := NewRedisWire(client)
	defer conn.Close()
	key, payload := []byte{'k', 0, '\r', '\n', 255}, []byte{0, 255, 128, '\r', '\n'}
	finished := make(chan error, 1)
	go func() {
		budget := 4096
		args, err := readRedisReply(bufio.NewReader(server), 0, &budget)
		if err == nil && !reflect.DeepEqual(args, []any{[]byte("DUMP"), key}) {
			err = io.ErrUnexpectedEOF
		}
		if err == nil {
			_, err = server.Write(append(append([]byte("$5\r\n"), payload...), '\r', '\n'))
		}
		finished <- err
	}()
	v, err := conn.Command([]byte("DUMP"), key)
	if err != nil || !bytes.Equal(v.([]byte), payload) {
		t.Fatal("binary response changed", err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestRedisKeySlotUsesBinaryHashTags(t *testing.T) {
	if RedisKeySlot([]byte("123456789")) != 12739 {
		t.Fatal("CRC16 test vector changed")
	}
	for _, key := range [][]byte{[]byte("before{user}after"), []byte("{user}"), append([]byte{0, 255}, []byte("{user}")...)} {
		if RedisKeySlot(key) != RedisKeySlot([]byte("user")) {
			t.Fatal("hash tag changed")
		}
	}
	if RedisKeySlot([]byte("{}user")) == RedisKeySlot([]byte("user")) {
		t.Fatal("empty tag incorrectly stripped")
	}
}

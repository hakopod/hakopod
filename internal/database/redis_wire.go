package database

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// RedisWire is a bounded RESP2 connection for copying immutable snapshots. It
// never retries writes or includes server replies (which may contain data) in errors.
type RedisWire struct {
	conn net.Conn
	in   *bufio.Reader
}

const RedisCopyMaxValue = 64 << 20

func NewRedisWire(conn net.Conn) *RedisWire {
	return &RedisWire{conn: conn, in: bufio.NewReaderSize(conn, 4096)}
}
func (r *RedisWire) Close() error { return r.conn.Close() }

func (r *RedisWire) Command(args ...[]byte) (any, error) {
	if len(args) == 0 || len(args) > 32 {
		return nil, fmt.Errorf("Redis command exceeds its argument limit")
	}
	if err := r.conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return nil, err
	}
	writer := bufio.NewWriterSize(r.conn, 4096)
	fmt.Fprintf(writer, "*%d\r\n", len(args))
	total := 0
	for _, arg := range args {
		total += len(arg)
		if total > RedisCopyMaxValue+2<<20 {
			return nil, fmt.Errorf("Redis command exceeds its byte limit")
		}
		fmt.Fprintf(writer, "$%d\r\n", len(arg))
		if _, err := writer.Write(arg); err != nil {
			return nil, err
		}
		io.WriteString(writer, "\r\n")
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}
	budget := RedisCopyMaxValue + 2<<20
	return readRedisReply(r.in, 0, &budget)
}

func readRedisReply(in *bufio.Reader, depth int, budget *int) (any, error) {
	if depth > 4 || *budget <= 0 {
		return nil, fmt.Errorf("Redis reply exceeds its limit")
	}
	line, err := in.ReadSlice('\n')
	if err != nil || len(line) < 3 || line[len(line)-2] != '\r' {
		return nil, fmt.Errorf("Redis reply framing is invalid")
	}
	*budget -= len(line)
	value := line[1 : len(line)-2]
	switch line[0] {
	case '+':
		return string(value), nil
	case '-':
		return nil, fmt.Errorf("Redis rejected a recovery command")
	case ':':
		n, err := strconv.ParseInt(string(value), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("Redis integer reply is invalid")
		}
		return n, nil
	case '$', '*':
		n, err := strconv.Atoi(string(value))
		if err != nil || n < -1 {
			return nil, fmt.Errorf("Redis reply length is invalid")
		}
		if n == -1 {
			return nil, nil
		}
		if line[0] == '$' {
			if n > RedisCopyMaxValue || n+2 > *budget {
				return nil, fmt.Errorf("Redis value exceeds the 64 MiB recovery limit")
			}
			buf := make([]byte, n+2)
			*budget -= len(buf)
			if _, err := io.ReadFull(in, buf); err != nil || !bytes.Equal(buf[n:], []byte("\r\n")) {
				return nil, fmt.Errorf("Redis bulk reply is truncated")
			}
			return buf[:n], nil
		}
		if n > 16384 || n*8 > *budget {
			return nil, fmt.Errorf("Redis array reply exceeds its limit")
		}
		*budget -= n * 8
		values := make([]any, n)
		for i := range values {
			values[i], err = readRedisReply(in, depth+1, budget)
			if err != nil {
				return nil, err
			}
		}
		return values, nil
	default:
		return nil, fmt.Errorf("Redis reply type is unsupported")
	}
}

// RedisKeySlot follows Redis Cluster's binary-safe hash-tag and CRC16 rules.
func RedisKeySlot(key []byte) int {
	if start := bytes.IndexByte(key, '{'); start >= 0 {
		if end := bytes.IndexByte(key[start+1:], '}'); end > 0 {
			key = key[start+1 : start+1+end]
		}
	}
	var crc uint16
	for _, b := range key {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return int(crc % 16384)
}

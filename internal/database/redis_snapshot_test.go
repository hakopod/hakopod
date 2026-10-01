package database

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"testing"
)

func TestRedisSnapshotStrictFraming(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		limit       int64
		valid       bool
	}{{"snapshot", "\n$13\r\nREDIS0012test", 100, true}, {"truncated", "$15\r\nREDIS0012test", 100, false}, {"oversized", "$101\r\n", 100, false}, {"diskless unexpected", "$EOF:unrequested\r\n", 100, false}, {"server error", "-ERR rejected\r\n", 100, false}} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer server.Close()
				line, _ := bufio.NewReader(server).ReadString('\n')
				if line == "SYNC\r\n" {
					_, _ = io.WriteString(server, tc.reply)
				}
			}()
			out := &bytes.Buffer{}
			err := NewRedisWire(client).DownloadSnapshot(context.Background(), out, tc.limit)
			_ = client.Close()
			<-done
			if (err == nil) != tc.valid {
				t.Fatal("unexpected snapshot result", err)
			}
			if tc.valid && out.String() != "REDIS0012test" {
				t.Fatal("snapshot bytes changed")
			}
		})
	}
}

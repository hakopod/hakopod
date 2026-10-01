package database

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"
)

// DownloadSnapshot uses the length-prefixed SYNC protocol without advertising
// diskless EOF capability. The caller authenticates over verified TLS first.
// Redis validates the staged RDB before it enters a managed backup archive.
func (r *RedisWire) DownloadSnapshot(ctx context.Context, out io.Writer, maxBytes int64) error {
	if maxBytes < 1 || maxBytes > 1<<42 {
		return fmt.Errorf("Redis snapshot size limit is invalid")
	}
	deadline := time.Now().Add(30 * time.Minute)
	if until, ok := ctx.Deadline(); ok && until.Before(deadline) {
		deadline = until
	}
	if err := r.conn.SetDeadline(deadline); err != nil {
		return err
	}
	if _, err := io.WriteString(r.conn, "SYNC\r\n"); err != nil {
		return err
	}
	for keepalive := 0; keepalive < 1800; keepalive++ {
		line, err := r.in.ReadSlice('\n')
		if err != nil {
			return fmt.Errorf("Redis snapshot header could not be read")
		}
		if len(line) == 1 {
			continue
		}
		if len(line) < 4 || line[0] != '$' || line[len(line)-2] != '\r' {
			return fmt.Errorf("Redis snapshot header is invalid")
		}
		size, err := strconv.ParseInt(string(line[1:len(line)-2]), 10, 64)
		if err != nil || size < 9 || size > maxBytes {
			return fmt.Errorf("Redis snapshot exceeds its accepted storage limit")
		}
		_, err = io.CopyN(out, r.in, size)
		return err
	}
	return fmt.Errorf("Redis snapshot did not start within its limit")
}

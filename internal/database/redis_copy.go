package database

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// CopyRedisSnapshot consumes only a disposable Redis process loaded from an RDB.
// Removing copied keys from that process makes SCAN duplicates harmless without
// keeping a set of every key in the management server's memory.
func CopyRedisSnapshot(ctx context.Context, source *RedisWire, target func([]byte, int) (*RedisWire, error), cluster bool) error {
	keys := 0
	for db := 0; db < 16; db++ {
		if _, err := source.Command([]byte("SELECT"), []byte(strconv.Itoa(db))); err != nil {
			return err
		}
		size, err := source.Command([]byte("DBSIZE"))
		if err != nil {
			return err
		}
		count, ok := size.(int64)
		if !ok || count < 0 {
			return fmt.Errorf("Redis snapshot key count is invalid")
		}
		if cluster && db != 0 && count != 0 {
			return fmt.Errorf("Redis Cluster recovery requires an archive containing only database 0")
		}
		if count == 0 {
			continue
		}
		cursor := []byte("0")
		for iterations := 0; ; iterations++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			if iterations >= 1000000 {
				return fmt.Errorf("Redis snapshot scan exceeded its limit")
			}
			v, err := source.Command([]byte("SCAN"), cursor, []byte("COUNT"), []byte("128"))
			if err != nil {
				return err
			}
			page, ok := v.([]any)
			if !ok || len(page) != 2 {
				return fmt.Errorf("Redis snapshot scan is invalid")
			}
			cursor, ok = page[0].([]byte)
			if !ok || len(cursor) > 20 {
				return fmt.Errorf("Redis snapshot cursor is invalid")
			}
			if _, err := strconv.ParseUint(string(cursor), 10, 64); err != nil {
				return fmt.Errorf("Redis snapshot cursor is invalid")
			}
			items, ok := page[1].([]any)
			if !ok {
				return fmt.Errorf("Redis snapshot key list is invalid")
			}
			for _, item := range items {
				key, ok := item.([]byte)
				if !ok || len(key) > 1<<20 {
					return fmt.Errorf("Redis key exceeds the 1 MiB recovery limit")
				}
				keys++
				if keys > 10000000 {
					return fmt.Errorf("Redis snapshot exceeds the ten million key recovery limit")
				}
				// The absolute deadline and opaque Redis serialization are read together.
				value, err := source.Command([]byte("EVAL"), []byte("return {redis.call('DUMP',KEYS[1]),redis.call('PEXPIRETIME',KEYS[1])}"), []byte("1"), key)
				if err != nil {
					return err
				}
				pair, ok := value.([]any)
				if !ok || len(pair) != 2 {
					return fmt.Errorf("Redis snapshot value is invalid")
				}
				expires, ok := pair[1].(int64)
				if !ok || expires < -2 {
					return fmt.Errorf("Redis snapshot expiry is invalid")
				}
				if pair[0] == nil || expires == -2 || (expires > 0 && expires <= time.Now().UnixMilli()) {
					continue
				}
				payload, ok := pair[0].([]byte)
				if !ok {
					return fmt.Errorf("Redis snapshot payload is invalid")
				}
				if expires == -1 {
					expires = 0
				}
				conn, err := target(key, db)
				if err != nil {
					return err
				}
				if _, err = conn.Command([]byte("RESTORE"), key, []byte(strconv.FormatInt(expires, 10)), payload, []byte("ABSTTL")); err != nil {
					return err
				}
				if _, err = source.Command([]byte("UNLINK"), key); err != nil {
					return err
				}
			}
			if string(cursor) == "0" {
				break
			}
		}
		size, err = source.Command([]byte("DBSIZE"))
		if err != nil || size != int64(0) {
			return fmt.Errorf("Redis snapshot still contains uncopied keys")
		}
	}
	return nil
}

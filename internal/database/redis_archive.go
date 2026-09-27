package database

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"io"
)

const RedisArchiveMagic = "HKREDIS1\n"
const RedisArchiveMaxBytes int64 = 64 << 30
const redisArchiveChunk = 64 << 10

type RedisArchive struct {
	SchemaVersion       int      `json:"schema_version"`
	DatabaseID          string   `json:"database_id"`
	Revision            int64    `json:"revision"`
	TopologyFingerprint string   `json:"topology_fingerprint"`
	Shards              []Member `json:"shards"`
}

func (a RedisArchive) validate() error {
	if a.SchemaVersion != 1 || len(a.DatabaseID) != 32 || a.Revision < 1 || len(a.Shards) < 1 || len(a.Shards) > 16 {
		return fmt.Errorf("Redis archive identity or shard count is invalid")
	}
	seen := map[string]bool{}
	for _, m := range a.Shards {
		if m.UID == "" || seen[m.UID] || m.Role != "primary" {
			return fmt.Errorf("Redis archive shard identity is invalid")
		}
		seen[m.UID] = true
	}
	return nil
}

// WriteRedisArchive frames bounded chunks so database snapshots can stream
// without keeping an entire shard in memory or disk on the management server.
func WriteRedisArchive(out io.Writer, a RedisArchive, dump func(Member, io.Writer) error) error {
	if err := a.validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(a)
	if err != nil || len(raw) > 16<<10 {
		return fmt.Errorf("Redis archive manifest exceeds its bound")
	}
	if _, err = io.WriteString(out, RedisArchiveMagic); err != nil {
		return err
	}
	if err = binary.Write(out, binary.BigEndian, uint32(len(raw))); err != nil {
		return err
	}
	if _, err = out.Write(raw); err != nil {
		return err
	}
	remaining := RedisArchiveMaxBytes
	for _, member := range a.Shards {
		framed := &redisArchiveWriter{out: out, digest: sha256.New(), remaining: &remaining}
		if err = dump(member, framed); err != nil {
			return err
		}
		if framed.written < 9 {
			return fmt.Errorf("Redis snapshot is empty")
		}
		if err = binary.Write(out, binary.BigEndian, uint32(0)); err != nil {
			return err
		}
		if _, err = out.Write(framed.digest.Sum(nil)); err != nil {
			return err
		}
	}
	return nil
}

type redisArchiveWriter struct {
	out       io.Writer
	digest    hash.Hash
	remaining *int64
	written   int64
}

func (w *redisArchiveWriter) Write(p []byte) (int, error) {
	total := len(p)
	if int64(total) > *w.remaining {
		return 0, fmt.Errorf("Redis archive exceeds its byte limit")
	}
	written := 0
	for len(p) > 0 {
		n := min(len(p), redisArchiveChunk)
		if err := binary.Write(w.out, binary.BigEndian, uint32(n)); err != nil {
			return written, err
		}
		if _, err := w.out.Write(p[:n]); err != nil {
			return written, err
		}
		_, _ = w.digest.Write(p[:n])
		p = p[n:]
		written += n
	}
	*w.remaining -= int64(total)
	w.written += int64(total)
	return total, nil
}

// ReadRedisArchive verifies framing, per-shard digests and EOF. The caller must
// stage each shard and complete verification before changing a restore target.
func ReadRedisArchive(in io.Reader, stage func(RedisArchive, Member, io.Reader) error) (RedisArchive, error) {
	var a RedisArchive
	magic := make([]byte, len(RedisArchiveMagic))
	if _, err := io.ReadFull(in, magic); err != nil || string(magic) != RedisArchiveMagic {
		return a, fmt.Errorf("Redis archive header is invalid")
	}
	var length uint32
	if err := binary.Read(in, binary.BigEndian, &length); err != nil || length == 0 || length > 16<<10 {
		return a, fmt.Errorf("Redis archive manifest length is invalid")
	}
	raw := make([]byte, length)
	if _, err := io.ReadFull(in, raw); err != nil {
		return a, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&a); err != nil {
		return a, fmt.Errorf("Redis archive manifest is invalid")
	}
	if err := a.validate(); err != nil {
		return a, err
	}
	remaining := RedisArchiveMaxBytes
	for _, member := range a.Shards {
		framed := &redisArchiveReader{in: in, digest: sha256.New(), remaining: &remaining}
		if err := stage(a, member, framed); err != nil {
			return a, err
		}
		if _, err := io.Copy(io.Discard, framed); err != nil {
			return a, err
		}
		expected := make([]byte, sha256.Size)
		if _, err := io.ReadFull(in, expected); err != nil {
			return a, fmt.Errorf("Redis archive shard digest is missing")
		}
		if framed.read < 9 || !bytes.Equal(expected, framed.digest.Sum(nil)) {
			return a, fmt.Errorf("Redis archive shard digest does not match")
		}
	}
	var extra [1]byte
	n, err := in.Read(extra[:])
	if n != 0 || err != io.EOF {
		return a, fmt.Errorf("Redis archive contains trailing data")
	}
	return a, nil
}

type redisArchiveReader struct {
	in        io.Reader
	digest    hash.Hash
	remaining *int64
	chunk     uint32
	done      bool
	read      int64
}

func (r *redisArchiveReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.done {
		return 0, io.EOF
	}
	if r.chunk == 0 {
		if err := binary.Read(r.in, binary.BigEndian, &r.chunk); err != nil {
			return 0, fmt.Errorf("Redis archive is truncated")
		}
		if r.chunk == 0 {
			r.done = true
			return 0, io.EOF
		}
		if r.chunk > redisArchiveChunk || int64(r.chunk) > *r.remaining {
			return 0, fmt.Errorf("Redis archive exceeds its byte limit")
		}
	}
	n, err := io.ReadFull(r.in, p[:min(len(p), int(r.chunk))])
	if n > 0 {
		_, _ = r.digest.Write(p[:n])
		r.chunk -= uint32(n)
		*r.remaining -= int64(n)
		r.read += int64(n)
	}
	return n, err
}

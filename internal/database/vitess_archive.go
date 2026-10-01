package database

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"regexp"
	"slices"
)

const vitessArchiveMagic = "HAKOPOD_VITESS_LOGICAL_V1\n"
const VitessArchiveMaxBytes int64 = 64 << 30
const vitessArchiveFrameMax = 64 << 10
const VitessServerVersion = "23.0.6"
const VitessMySQLVersion = "8.4.6"

// Every shard has its own read-lock snapshot. This archive does not claim a
// common transaction boundary across shards and never contains mysql.* users.
type VitessArchive struct {
	SchemaVersion       int             `json:"schema_version"`
	DatabaseID          string          `json:"database_id"`
	Revision            int64           `json:"revision"`
	Version             string          `json:"version"`
	ServerVersion       string          `json:"server_version"`
	MySQLVersion        string          `json:"mysql_version"`
	Mode                string          `json:"mode"`
	Shards              []string        `json:"shards"`
	Tables              []VitessTable   `json:"tables,omitempty"`
	VSchema             json.RawMessage `json:"vschema"`
	Consistency         string          `json:"consistency"`
	TopologyFingerprint string          `json:"topology_fingerprint"`
}

func (a VitessArchive) Validate() error {
	if a.SchemaVersion != 1 || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(a.DatabaseID) || a.Revision < 1 || a.Version != VitessVersion || a.ServerVersion != VitessServerVersion || a.MySQLVersion != VitessMySQLVersion || a.Consistency != "per-shard-read-lock" || len(a.TopologyFingerprint) != 64 {
		return fmt.Errorf("Vitess archive identity, version or consistency is invalid")
	}
	if _, err := hex.DecodeString(a.TopologyFingerprint); err != nil {
		return fmt.Errorf("Vitess archive topology fingerprint is invalid")
	}
	s := Spec{Engine: "vitess", Version: a.Version, Mode: a.Mode, Shards: len(a.Shards), CPU: "500m", Memory: "1Gi", StorageGiB: 1, TLS: &TLSConfig{Mode: "required"}, Vitess: &VitessConfig{Tables: a.Tables}}
	if s.Mode == "cluster" {
		s.Replicas = 1
	}
	if !slices.Equal(s.VitessShardNames(), a.Shards) {
		return fmt.Errorf("Vitess archive shard map is invalid")
	}
	want, err := s.VitessVSchema()
	if err != nil {
		return err
	}
	var compact bytes.Buffer
	if json.Compact(&compact, a.VSchema) != nil || !bytes.Equal(compact.Bytes(), want) {
		return fmt.Errorf("Vitess archive routing schema does not match its table contract")
	}
	return nil
}

type vitessFrameWriter struct {
	out       io.Writer
	digest    hash.Hash
	remaining *int64
	bytes     int64
}

func vitessWriteFull(out io.Writer, data []byte) error {
	n, err := out.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}

func (w *vitessFrameWriter) Write(data []byte) (int, error) {
	written := 0
	for len(data) != 0 {
		n := min(len(data), vitessArchiveFrameMax)
		if int64(n) > *w.remaining {
			return written, fmt.Errorf("Vitess archive exceeds its byte limit")
		}
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(n))
		if err := vitessWriteFull(w.out, size[:]); err != nil {
			return written, err
		}
		if err := vitessWriteFull(w.out, data[:n]); err != nil {
			return written, err
		}
		_, _ = w.digest.Write(data[:n])
		*w.remaining -= int64(n)
		w.bytes += int64(n)
		written += n
		data = data[n:]
	}
	return written, nil
}

// Framing permits bounded streaming from mysqldump without staging a second
// full copy beside the source database. The outer backup service encrypts and
// authenticates the stream; per-shard SHA-256 detects framing corruption too.
func WriteVitessArchive(out io.Writer, a VitessArchive, dump func(int, io.Writer) error) error {
	if err := a.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(a)
	if err != nil || len(raw) > 64<<10 {
		return fmt.Errorf("Vitess archive metadata exceeds its bound")
	}
	if err = vitessWriteFull(out, []byte(vitessArchiveMagic)); err != nil {
		return err
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(raw)))
	if err = vitessWriteFull(out, size[:]); err != nil {
		return err
	}
	if err = vitessWriteFull(out, raw); err != nil {
		return err
	}
	remaining := VitessArchiveMaxBytes
	for i := range a.Shards {
		writer := &vitessFrameWriter{out: out, digest: sha256.New(), remaining: &remaining}
		if err = dump(i, writer); err != nil {
			return err
		}
		if writer.bytes == 0 {
			return fmt.Errorf("Vitess shard capture is empty")
		}
		if err = vitessWriteFull(out, []byte{0, 0, 0, 0}); err != nil {
			return err
		}
		if err = vitessWriteFull(out, writer.digest.Sum(nil)); err != nil {
			return err
		}
	}
	return nil
}

type vitessFrameReader struct {
	in        io.Reader
	digest    hash.Hash
	remaining *int64
	frame     uint32
	bytes     int64
	done      bool
}

func (r *vitessFrameReader) Read(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	if r.done {
		return 0, io.EOF
	}
	if r.frame == 0 {
		var size [4]byte
		if _, err := io.ReadFull(r.in, size[:]); err != nil {
			return 0, fmt.Errorf("Vitess archive frame was truncated")
		}
		r.frame = binary.BigEndian.Uint32(size[:])
		if r.frame == 0 {
			r.done = true
			return 0, io.EOF
		}
		if r.frame > vitessArchiveFrameMax || int64(r.frame) > *r.remaining {
			return 0, fmt.Errorf("Vitess archive frame exceeds its byte limit")
		}
	}
	n := min(len(data), int(r.frame))
	count, err := io.ReadFull(r.in, data[:n])
	r.frame -= uint32(count)
	*r.remaining -= int64(count)
	r.bytes += int64(count)
	_, _ = r.digest.Write(data[:count])
	if err != nil {
		return count, fmt.Errorf("Vitess archive payload was truncated")
	}
	return count, nil
}

// The stage callback may write files only. Restore SQL must wait for this
// function to validate every shard digest and the authenticated outer EOF.
func ReadVitessArchive(in io.Reader, stage func(VitessArchive, int, io.Reader) error) (VitessArchive, error) {
	var a VitessArchive
	magic := make([]byte, len(vitessArchiveMagic))
	if _, err := io.ReadFull(in, magic); err != nil || string(magic) != vitessArchiveMagic {
		return a, fmt.Errorf("Vitess archive header is invalid")
	}
	var size uint32
	if err := binary.Read(in, binary.BigEndian, &size); err != nil || size == 0 || size > 64<<10 {
		return a, fmt.Errorf("Vitess archive metadata exceeds its bound")
	}
	raw := make([]byte, size)
	if _, err := io.ReadFull(in, raw); err != nil {
		return a, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&a) != nil || decoder.Decode(new(any)) != io.EOF {
		return a, fmt.Errorf("Vitess archive metadata is invalid")
	}
	if err := a.Validate(); err != nil {
		return a, err
	}
	remaining := VitessArchiveMaxBytes
	for i := range a.Shards {
		reader := &vitessFrameReader{in: in, digest: sha256.New(), remaining: &remaining}
		if err := stage(a, i, reader); err != nil {
			return a, err
		}
		if !reader.done || reader.bytes == 0 {
			return a, fmt.Errorf("Vitess shard staging was incomplete")
		}
		digest := make([]byte, sha256.Size)
		if _, err := io.ReadFull(in, digest); err != nil || !bytes.Equal(reader.digest.Sum(nil), digest) {
			return a, fmt.Errorf("Vitess shard integrity check failed")
		}
	}
	var extra [1]byte
	if n, err := in.Read(extra[:]); n != 0 || err != io.EOF {
		return a, fmt.Errorf("Vitess archive has trailing data")
	}
	return a, nil
}

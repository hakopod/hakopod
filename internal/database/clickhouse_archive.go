package database

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
)

const ClickHouseArchiveMaxBytes int64 = 64 << 30

type ClickHouseArchive struct {
	SchemaVersion int    `json:"schema_version"`
	DatabaseID    string `json:"database_id"`
	Revision      int64  `json:"revision"`
	Version       string `json:"version"`
	Mode          string `json:"mode"`
	Shards        int    `json:"shards"`
}

func (a ClickHouseArchive) validate() error {
	if a.SchemaVersion != 1 || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(a.DatabaseID) || a.Revision < 1 || a.Version != "26.3" || a.Shards < 1 || a.Shards > 8 || a.Mode != "standalone" && a.Mode != "cluster" || a.Mode == "standalone" && a.Shards != 1 {
		return fmt.Errorf("ClickHouse archive identity or layout is invalid")
	}
	return nil
}

// Native ZIP files are staged on bounded database volumes. This outer archive
// records their order without loading them into the management process.
func WriteClickHouseArchive(out io.Writer, a ClickHouseArchive, size func(int) (int64, error), dump func(int, io.Writer) error) error {
	if err := a.validate(); err != nil {
		return err
	}
	tw := tar.NewWriter(out)
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if err = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(raw)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err = tw.Write(raw); err != nil {
		return err
	}
	remaining := ClickHouseArchiveMaxBytes
	for shard := 0; shard < a.Shards; shard++ {
		length, err := size(shard)
		if err != nil {
			return err
		}
		if length < 22 || length > remaining {
			return fmt.Errorf("ClickHouse shard archive exceeds its byte limit")
		}
		remaining -= length
		if err = tw.WriteHeader(&tar.Header{Name: strconv.Itoa(shard) + ".zip", Mode: 0600, Size: length, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if err = dump(shard, tw); err != nil {
			return err
		}
	}
	return tw.Close()
}

// The callback may stage bytes only. Restore starts after all members, archive
// framing and the authenticated encrypted stream have passed validation.
func ReadClickHouseArchive(in io.Reader, stage func(ClickHouseArchive, int, int64, io.Reader) error) (ClickHouseArchive, error) {
	var a ClickHouseArchive
	tr := tar.NewReader(in)
	head, err := tr.Next()
	if err != nil || head.Name != "manifest.json" || head.Typeflag != tar.TypeReg || head.Size < 1 || head.Size > 4096 {
		return a, fmt.Errorf("ClickHouse archive manifest is invalid")
	}
	raw, err := io.ReadAll(tr)
	if err != nil {
		return a, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&a) != nil {
		return a, fmt.Errorf("ClickHouse archive manifest is invalid")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return a, fmt.Errorf("ClickHouse archive manifest has trailing data")
	}
	if err = a.validate(); err != nil {
		return a, err
	}
	remaining := ClickHouseArchiveMaxBytes
	for shard := 0; shard < a.Shards; shard++ {
		head, err = tr.Next()
		if err != nil || head.Name != strconv.Itoa(shard)+".zip" || head.Typeflag != tar.TypeReg || head.Size < 22 || head.Size > remaining {
			return a, fmt.Errorf("ClickHouse shard archive entry is invalid")
		}
		remaining -= head.Size
		if err = stage(a, shard, head.Size, tr); err != nil {
			return a, err
		}
		if n, err := io.Copy(io.Discard, tr); err != nil || n != 0 {
			return a, fmt.Errorf("ClickHouse staged shard is incomplete")
		}
	}
	if _, err = tr.Next(); err != io.EOF {
		return a, fmt.Errorf("ClickHouse archive contains unexpected entries")
	}
	var end [1]byte
	if n, err := in.Read(end[:]); n != 0 || err != io.EOF {
		return a, fmt.Errorf("ClickHouse archive contains trailing data")
	}
	return a, nil
}

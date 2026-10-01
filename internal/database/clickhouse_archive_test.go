package database

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestClickHouseArchiveBoundsAndIdentity(t *testing.T) {
	a := ClickHouseArchive{SchemaVersion: 1, DatabaseID: strings.Repeat("a", 32), Revision: 1, Version: "26.3", Mode: "cluster", Shards: 2}
	var archive bytes.Buffer
	data := bytes.Repeat([]byte{0x50, 0x4b, 0x03, 0x04}, 100)
	if err := WriteClickHouseArchive(&archive, a, func(int) (int64, error) { return int64(len(data)), nil }, func(_ int, w io.Writer) error { _, err := w.Write(data); return err }); err != nil {
		t.Fatal(err)
	}
	staged := 0
	read, err := ReadClickHouseArchive(bytes.NewReader(archive.Bytes()), func(got ClickHouseArchive, shard int, size int64, r io.Reader) error {
		if got != a || shard != staged || size != int64(len(data)) {
			t.Fatal("manifest identity changed")
		}
		raw, err := io.ReadAll(r)
		if !bytes.Equal(raw, data) {
			t.Fatal("shard bytes changed")
		}
		staged++
		return err
	})
	if err != nil || read != a || staged != 2 {
		t.Fatal("archive round trip failed", err)
	}
	for _, bad := range [][]byte{archive.Bytes()[:100], append(append([]byte{}, archive.Bytes()...), 1), append([]byte("invalid"), archive.Bytes()[7:]...)} {
		if _, err := ReadClickHouseArchive(bytes.NewReader(bad), func(_ ClickHouseArchive, _ int, _ int64, r io.Reader) error {
			_, err := io.Copy(io.Discard, r)
			return err
		}); err == nil {
			t.Fatal("invalid archive accepted")
		}
	}
	if _, err := ReadClickHouseArchive(bytes.NewReader(archive.Bytes()), func(ClickHouseArchive, int, int64, io.Reader) error { return nil }); err == nil {
		t.Fatal("incomplete staging accepted")
	}
	if err := WriteClickHouseArchive(io.Discard, a, func(int) (int64, error) { return ClickHouseArchiveMaxBytes + 1, nil }, func(int, io.Writer) error { return nil }); err == nil {
		t.Fatal("oversized shard accepted")
	}
}

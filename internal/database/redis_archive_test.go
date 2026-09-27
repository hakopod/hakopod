package database

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestRedisArchiveIntegrityAndBounds(t *testing.T) {
	manifest := RedisArchive{SchemaVersion: 1, DatabaseID: strings.Repeat("a", 32), Revision: 1, Shards: []Member{{Name: "shard-0", UID: "one", Role: "primary"}, {Name: "shard-1", UID: "two", Role: "primary"}}}
	snapshot := append([]byte("REDIS0012"), bytes.Repeat([]byte{0, 10, 255}, 30000)...)
	var out bytes.Buffer
	if err := WriteRedisArchive(&out, manifest, func(_ Member, w io.Writer) error { _, err := w.Write(snapshot); return err }); err != nil {
		t.Fatal(err)
	}
	count := 0
	actual, err := ReadRedisArchive(bytes.NewReader(out.Bytes()), func(_ RedisArchive, _ Member, r io.Reader) error {
		b, err := io.ReadAll(r)
		if !bytes.Equal(b, snapshot) {
			t.Fatal("snapshot changed")
		}
		count++
		return err
	})
	if err != nil || count != 2 || actual.DatabaseID != manifest.DatabaseID {
		t.Fatal("round trip", err)
	}
	for _, length := range []int{0, 8, 20, len(out.Bytes()) - 1, len(out.Bytes()) / 2} {
		if _, err := ReadRedisArchive(bytes.NewReader(out.Bytes()[:length]), func(_ RedisArchive, _ Member, r io.Reader) error { _, err := io.Copy(io.Discard, r); return err }); err == nil {
			t.Fatalf("truncation %d accepted", length)
		}
	}
	corrupt := bytes.Clone(out.Bytes())
	corrupt[len(corrupt)-1] ^= 1
	if _, err := ReadRedisArchive(bytes.NewReader(corrupt), func(_ RedisArchive, _ Member, r io.Reader) error { _, err := io.Copy(io.Discard, r); return err }); err == nil {
		t.Fatal("corrupt digest accepted")
	}
	trailing := append(bytes.Clone(out.Bytes()), 'x')
	if _, err := ReadRedisArchive(bytes.NewReader(trailing), func(_ RedisArchive, _ Member, r io.Reader) error { _, err := io.Copy(io.Discard, r); return err }); err == nil {
		t.Fatal("trailing bytes accepted")
	}
}
func TestDatabaseCredentialsAreResourceBound(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	password := bytes.Repeat([]byte{2}, 64)
	d := Resource{ID: strings.Repeat("a", 32)}
	var err error
	d.EncryptedCredentials, err = SealCredentials(key, d.ID, password)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := OpenCredentials(key, d)
	if err != nil || !bytes.Equal(plain, password) {
		t.Fatal("round trip", err)
	}
	d.ID = strings.Repeat("b", 32)
	if _, err = OpenCredentials(key, d); err == nil {
		t.Fatal("credentials moved across resources")
	}
}

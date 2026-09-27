package backup

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
)

func postgresImportHeader(version string) []byte {
	b := bytes.NewBuffer([]byte{'P', 'G', 'D', 'M', 'P', 1, 16, 0, 4, 8, 1, 0})
	integer := func(n int) { b.WriteByte(0); _ = binary.Write(b, binary.LittleEndian, uint32(n)) }
	for n := 0; n < 7; n++ {
		integer(0)
	}
	for _, s := range []string{"app", version, "17.6"} {
		integer(len(s))
		b.WriteString(s)
	}
	return b.Bytes()
}
func TestImportArchiveVersionEvidence(t *testing.T) {
	for _, version := range []string{"17.6", "18.0"} {
		raw := postgresImportHeader(version)
		reader := bufio.NewReaderSize(bytes.NewReader(raw), 32<<10)
		got, err := postgresArchiveVersion(reader)
		if err != nil || got != strings.Split(version, ".")[0] {
			t.Fatal(got, err)
		}
		after, _ := io.ReadAll(reader)
		if !bytes.Equal(raw, after) {
			t.Fatal("header parser consumed archive bytes")
		}
	}
	for _, raw := range [][]byte{nil, []byte("PGDMP"), postgresImportHeader("bad")} {
		if _, err := postgresArchiveVersion(bufio.NewReader(bytes.NewReader(raw))); err == nil {
			t.Fatal("bad PostgreSQL header accepted")
		}
	}
	rdb := func(version string) []byte {
		return append([]byte("REDIS0012\xfa\x09redis-ver"), append([]byte{byte(len(version))}, []byte(version)...)...)
	}
	for _, version := range []string{"7.4.0", "8.0.4"} {
		err := redisArchiveVersion(bufio.NewReader(bytes.NewReader(rdb(version))))
		if (err == nil) != (version == "8.0.4") {
			t.Fatal("Redis major not checked", version, err)
		}
	}
}
func TestImportEncryptedReadbackAndChecksum(t *testing.T) {
	d, _, key := testDestination(t)
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			raw := postgresImportHeader("17.6")
			version := "17"
			if engine == "redis" {
				raw = []byte("REDIS0012\xfa\x09redis-ver\x058.0.4\xff")
				version = "8"
			}
			sum := sha256.Sum256(raw)
			input := Import{ID: strings.Repeat("b", 32), Spec: ImportSpec{DestinationID: d.ID, SourceName: "docker-fixture", Engine: engine, SourceVersion: version, CapturedAt: time.Now().Add(-time.Minute), Bytes: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])}}
			objects := &testObjects{}
			service := &Service{Repo: &testRepository{destination: d}, CredentialKey: key, StateDir: t.TempDir(), ObjectStore: func(Destination, Credentials) ObjectStore { return objects }}
			if err := os.Chmod(service.StateDir, 0700); err != nil {
				t.Fatal(err)
			}
			a, err := service.ImportArchive(context.Background(), input, bytes.NewReader(raw))
			if err != nil || a.VerifiedAt == nil {
				t.Fatal("verified import", err)
			}
			if bytes.Contains(objects.data, raw) {
				t.Fatal("plaintext uploaded")
			}
			err = service.withVerifiedArchive(context.Background(), *a, d, func(r io.Reader) error {
				if engine == "redis" {
					_, e := database.ReadRedisArchive(r, func(_ database.RedisArchive, _ database.Member, part io.Reader) error {
						got, e := io.ReadAll(part)
						if !bytes.Equal(got, raw) {
							t.Error("RDB changed")
						}
						return e
					})
					return e
				}
				got, e := io.ReadAll(r)
				if !bytes.Equal(got, raw) {
					t.Error("dump changed")
				}
				return e
			})
			if err != nil {
				t.Fatal(err)
			}
			input.Spec.SHA256 = strings.Repeat("0", 64)
			if _, err = service.ImportArchive(context.Background(), input, bytes.NewReader(raw)); err == nil {
				t.Fatal("checksum mismatch accepted")
			}
			input.Spec.SourceVersion = "18"
			if _, err = service.ImportArchive(context.Background(), input, bytes.NewReader(raw)); err == nil {
				t.Fatal("wrong source version accepted")
			}
		})
	}
}

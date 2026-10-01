package database

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestOracleArchiveIntegrityAndStaging(t *testing.T) {
	payload := []byte{0, 255, 1, 4, 0, 2}
	manifest := OracleArchive{SchemaVersion: 1, DatabaseID: strings.Repeat("a", 32), Revision: 1, Version: "23.26", Edition: "free", SCN: 123, Bytes: int64(len(payload))}
	var archive bytes.Buffer
	if err := WriteOracleArchive(&archive, manifest, func(out io.Writer) error { _, err := out.Write(payload); return err }); err != nil {
		t.Fatal(err)
	}
	var staged bytes.Buffer
	got, err := ReadOracleArchive(bytes.NewReader(archive.Bytes()), func(a OracleArchive, r io.Reader) error { _, err := io.Copy(&staged, r); return err })
	if err != nil || got != manifest || !bytes.Equal(staged.Bytes(), payload) {
		t.Fatal("Oracle archive did not preserve binary data", err)
	}
	for _, size := range []int{0, 10, len(archive.Bytes()) - 1} {
		if _, err = ReadOracleArchive(bytes.NewReader(archive.Bytes()[:size]), func(a OracleArchive, r io.Reader) error { _, err := io.Copy(io.Discard, r); return err }); err == nil {
			t.Fatal("truncated archive admitted")
		}
	}
	corrupt := append([]byte{}, archive.Bytes()...)
	corrupt[len(corrupt)-sha256DigestBytes-1] ^= 0xff
	for _, data := range [][]byte{corrupt, append(archive.Bytes(), 0)} {
		if _, err = ReadOracleArchive(bytes.NewReader(data), func(a OracleArchive, r io.Reader) error { _, err := io.Copy(io.Discard, r); return err }); err == nil {
			t.Fatal("corrupt or trailing bytes admitted")
		}
	}
	if _, err = ReadOracleArchive(bytes.NewReader(archive.Bytes()), func(OracleArchive, io.Reader) error { return nil }); err == nil {
		t.Fatal("partial staging admitted")
	}
}

const sha256DigestBytes = 32

func TestOracleArchiveEditionVersionContract(t *testing.T) {
	for _, test := range []struct {
		edition, version string
		allowed          bool
	}{
		{"free", "23.26", true}, {"enterprise", "19", true}, {"enterprise", "23.26", true},
		{"free", "19", false}, {"enterprise", "21", false}, {"express", "23.26", false},
		{"", "23.26", false}, {"enterprise", "19.3", false},
	} {
		a := OracleArchive{SchemaVersion: 1, DatabaseID: strings.Repeat("a", 32), Revision: 1, Version: test.version, Edition: test.edition, SCN: 1, Bytes: 1}
		var output bytes.Buffer
		err := WriteOracleArchive(&output, a, func(w io.Writer) error { _, err := w.Write([]byte{1}); return err })
		if (err == nil) != test.allowed {
			t.Fatalf("%s %s archive acceptance: %v", test.edition, test.version, err)
		}
		if !test.allowed && output.Len() != 0 {
			t.Fatal("invalid Oracle archive wrote bytes")
		}
	}
}

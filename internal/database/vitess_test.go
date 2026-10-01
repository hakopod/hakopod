package database

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"
)

func testVitessSpec() Spec {
	return Spec{SchemaVersion: 1, Name: "orders", Engine: "vitess", Version: "23", Mode: "cluster", Shards: 2, Replicas: 1, CPU: "500m", Memory: "1Gi", StorageGiB: 1, TLS: &TLSConfig{Mode: "required"}, Vitess: &VitessConfig{BackupDestinationID: strings.Repeat("c", 32), BackupDestinationRevision: 1, Tables: []VitessTable{{Name: "orders", ShardingColumn: "tenant_id"}}}}
}

func TestVitessRoutingContract(t *testing.T) {
	s := testVitessSpec()
	if err := s.ValidateVitess(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.VitessShardNames(), ","); got != "-80,80-" {
		t.Fatalf("unexpected shard map %s", got)
	}
	for _, mutate := range []func(*Spec){
		func(s *Spec) { s.Shards = 3 }, func(s *Spec) { s.TLS = nil }, func(s *Spec) { s.Version = "24" }, func(s *Spec) { s.Replicas = 0 }, func(s *Spec) { s.Memory = "512Mi" }, func(s *Spec) { s.Vitess = nil }, func(s *Spec) { s.Vitess.Tables[0].ShardingColumn = "id; DROP TABLE orders" },
	} {
		bad := testVitessSpec()
		mutate(&bad)
		if bad.ValidateVitess() == nil {
			t.Errorf("accepted invalid spec: %+v", bad)
		}
	}
	s.Shards, s.Mode, s.Replicas = 1, "standalone", 0
	s.Vitess.Tables = nil
	if err := s.ValidateVitess(); err != nil {
		t.Fatal(err)
	}
	if s.VitessTopologyMembers() != 3 || s.VitessGateways() != 1 || s.VitessOrchestrators() != 1 {
		t.Fatal("standalone omitted required control components")
	}
}

func testVitessArchive(t *testing.T) VitessArchive {
	t.Helper()
	s := testVitessSpec()
	vschema, err := s.VitessVSchema()
	if err != nil {
		t.Fatal(err)
	}
	return VitessArchive{SchemaVersion: 1, DatabaseID: strings.Repeat("a", 32), Revision: 1, Version: VitessVersion, ServerVersion: VitessServerVersion, MySQLVersion: VitessMySQLVersion, Mode: s.Mode, Shards: s.VitessShardNames(), Tables: s.Vitess.Tables, VSchema: vschema, Consistency: "per-shard-read-lock", TopologyFingerprint: strings.Repeat("b", 64)}
}

func TestVitessArchiveRoundTripAndIntegrity(t *testing.T) {
	a := testVitessArchive(t)
	data := [][]byte{bytes.Repeat([]byte("INSERT\x00binary\xff\n"), 9000), []byte("second shard payload")}
	var out bytes.Buffer
	if err := WriteVitessArchive(&out, a, func(i int, w io.Writer) error { _, err := w.Write(data[i]); return err }); err != nil {
		t.Fatal(err)
	}
	stage := func(_ VitessArchive, i int, r io.Reader) error {
		actual, err := io.ReadAll(r)
		if err == nil && !bytes.Equal(actual, data[i]) {
			t.Error("shard data changed")
		}
		return err
	}
	if _, err := ReadVitessArchive(bytes.NewReader(out.Bytes()), stage); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func([]byte) []byte{
		func(b []byte) []byte { b[len(b)-1] ^= 1; return b },
		func(b []byte) []byte { return b[:len(b)-1] },
		func(b []byte) []byte { return append(b, 1) },
		func(b []byte) []byte {
			header := binary.BigEndian.Uint32(b[len(vitessArchiveMagic):])
			offset := len(vitessArchiveMagic) + 4 + int(header)
			binary.BigEndian.PutUint32(b[offset:], vitessArchiveFrameMax+1)
			return b
		},
	} {
		bad := change(append([]byte(nil), out.Bytes()...))
		if _, err := ReadVitessArchive(bytes.NewReader(bad), func(_ VitessArchive, _ int, r io.Reader) error { _, err := io.Copy(io.Discard, r); return err }); err == nil {
			t.Error("accepted corrupt archive")
		}
	}
	if _, err := ReadVitessArchive(bytes.NewReader(out.Bytes()), func(_ VitessArchive, _ int, _ io.Reader) error { return nil }); err == nil {
		t.Fatal("accepted unconsumed staging stream")
	}
}

func TestVitessArchiveRoutingMustMatch(t *testing.T) {
	a := testVitessArchive(t)
	a.VSchema = []byte(`{"sharded":false}`)
	if a.Validate() == nil {
		t.Fatal("accepted shard routing mismatch")
	}
	a = testVitessArchive(t)
	a.Shards[0] = "../mysql"
	if a.Validate() == nil {
		t.Fatal("accepted invalid shard identity")
	}
	a = testVitessArchive(t)
	a.Consistency = "global-snapshot"
	if a.Validate() == nil {
		t.Fatal("accepted unsupported consistency claim")
	}
}

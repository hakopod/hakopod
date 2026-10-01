package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
)

type archivePipelineRuntime struct {
	testRuntime
	version string
}

func (r *archivePipelineRuntime) Resolve(_ context.Context, source Source) (Target, error) {
	return Target{Source: source, Revision: 3, SourceVersion: r.version, Available: true}, nil
}

func TestManagedArchiveCapturePipeline(t *testing.T) {
	// These are framing fixtures, not native database dumps. Native recovery is
	// covered by the separate real-cluster acceptance tests.
	payload := []byte("development framing fixture: binary \x00\x80\xff and Unicode नमस्ते")
	sourceID := strings.Repeat("c", 32)
	for _, engine := range []string{"oracle", "clickhouse", "vitess"} {
		t.Run(engine, func(t *testing.T) {
			var archive bytes.Buffer
			var version, format string
			var err error
			writePayload := func(w io.Writer) error {
				_, e := w.Write(payload)
				return e
			}
			if engine == "oracle" {
				version, format = "23.26", "age-v1+oracle-datapump-v1"
				err = database.WriteOracleArchive(&archive, database.OracleArchive{
					SchemaVersion: 1, DatabaseID: sourceID, Revision: 3,
					Version: version, Edition: "free", SCN: 42, Bytes: int64(len(payload)),
				}, writePayload)
			} else if engine == "vitess" {
				version, format = "23", "age-v1+vitess-logical-v1"
				err = database.WriteVitessArchive(&archive, database.VitessArchive{
					SchemaVersion: 1, DatabaseID: sourceID, Revision: 3,
					Version: version, ServerVersion: database.VitessServerVersion, MySQLVersion: database.VitessMySQLVersion,
					Mode: "standalone", Shards: []string{"-"}, VSchema: []byte(`{"sharded":false}`),
					Consistency: "per-shard-read-lock", TopologyFingerprint: strings.Repeat("a", 64),
				}, func(_ int, w io.Writer) error { return writePayload(w) })
			} else {
				version, format = "26.3", "age-v1+clickhouse-shards-v1"
				err = database.WriteClickHouseArchive(&archive, database.ClickHouseArchive{
					SchemaVersion: 1, DatabaseID: sourceID, Revision: 3,
					Version: version, Mode: "standalone", Shards: 1,
				}, func(int) (int64, error) { return int64(len(payload)), nil }, func(_ int, w io.Writer) error { return writePayload(w) })
			}
			if err != nil {
				t.Fatal("build framing fixture", err)
			}
			for _, scenario := range []string{"success", "wrong format", "runtime failure after output", "corrupt download"} {
				t.Run(scenario, func(t *testing.T) {
					d, _, key := testDestination(t)
					runtime := &archivePipelineRuntime{testRuntime: testRuntime{data: archive.String()}, version: version}
					if scenario == "wrong format" {
						runtime.data = "PGDMPwrong engine framing fixture"
					}
					if scenario == "runtime failure after output" {
						runtime.failure = errors.New("development fixture staging cleanup failed")
					}
					objects := &corruptReadObjects{}
					service := &Service{
						Repo: &testRepository{destination: d}, Runtime: runtime, CredentialKey: key,
						StateDir: filepath.Join(t.TempDir(), "private"),
						ObjectStore: func(Destination, Credentials) ObjectStore {
							if scenario == "corrupt download" {
								return objects
							}
							return &objects.testObjects
						},
					}
					ctx := context.Background()
					source := Source{Kind: "managed_database", ManagedDatabaseID: sourceID, Engine: engine}
					artifact, err := service.create(ctx, Job{
						ID: strings.Repeat("d", 32), Kind: "backup", DestinationID: d.ID, Source: source,
					})
					if runtime.restoreCalls != 0 {
						t.Fatal("capture verification invoked Runtime.Restore")
					}
					if scenario != "success" {
						if err == nil || artifact != nil {
							t.Fatal("failed capture returned a successful artifact", err)
						}
						if scenario == "corrupt download" && objects.deleted == 0 {
							t.Fatal("failed downloaded verification did not delete the uploaded object")
						}
						return
					}
					if err != nil || artifact == nil {
						t.Fatal("valid framed archive capture failed", err)
					}
					if artifact.Format != format || artifact.SourceVersion != version || artifact.SourceRevision != 3 || artifact.Source.ManagedDatabaseID != sourceID || artifact.Source.Engine != engine {
						t.Fatal("artifact lost source identity, format or version")
					}
					if artifact.CapturedAt == nil || artifact.VerifiedAt == nil || artifact.CapturedAt.IsZero() || artifact.VerifiedAt.Before(*artifact.CapturedAt) {
						t.Fatal("artifact lacks ordered capture and verification timestamps")
					}
					if len(objects.data) == 0 || bytes.Contains(objects.data, payload) {
						t.Fatal("upload was empty or exposed plaintext")
					}
					consumed := false
					err = service.withVerifiedArchive(ctx, *artifact, d, func(r io.Reader) error {
						consumed = true
						plaintext, e := io.ReadAll(r)
						if e != nil {
							return e
						}
						if !bytes.Equal(plaintext, archive.Bytes()) {
							t.Error("encrypted round trip changed archive bytes")
						}
						checkPayload := func(part io.Reader) error {
							got, e := io.ReadAll(part)
							if !bytes.Equal(got, payload) {
								t.Error("decoded archive changed payload bytes")
							}
							return e
						}
						if engine == "oracle" {
							_, e = database.ReadOracleArchive(bytes.NewReader(plaintext), func(_ database.OracleArchive, part io.Reader) error { return checkPayload(part) })
						} else if engine == "vitess" {
							_, e = database.ReadVitessArchive(bytes.NewReader(plaintext), func(_ database.VitessArchive, _ int, part io.Reader) error { return checkPayload(part) })
						} else {
							_, e = database.ReadClickHouseArchive(bytes.NewReader(plaintext), func(_ database.ClickHouseArchive, _ int, _ int64, part io.Reader) error { return checkPayload(part) })
						}
						return e
					})
					if err != nil || !consumed || runtime.restoreCalls != 0 {
						t.Fatal("verified plaintext recovery failed or invoked Runtime.Restore", err)
					}
				})
			}
		})
	}
}

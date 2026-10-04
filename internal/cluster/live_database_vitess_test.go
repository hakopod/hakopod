package cluster

import (
	"bytes"
	"context"
	"crypto/des"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/database"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Each entry must use a separate fixture-only S3 principal whose provider
// policy is scoped to this database. The file is supplied on the development
// VM; neither credentials nor customer storage belong in source or test logs.
type vitessLiveStorage struct {
	DatabaseID            string             `json:"database_id"`
	Dedicated             bool               `json:"dedicated"`
	Destination           backup.Destination `json:"destination"`
	Credentials           backup.Credentials `json:"credentials"`
	ApprovedEndpointCIDRs []netip.Prefix     `json:"approved_endpoint_cidrs"`
}

type vitessLiveFixtures struct {
	c       *Client
	storage map[string]vitessLiveStorage
	revoked map[string]bool
}

var vitessDedicatedFixtureNodes = []string{
	"k3d-hakopod-vitess-worker-0",
	"k3d-hakopod-vitess-worker-1",
	"k3d-hakopod-vitess-worker-2",
}

func vitessNativeFixtureDatabasePolicy(fixtures map[string]vitessLiveStorage) DatabasePolicyResolver {
	approved := make(map[string]backup.Destination, len(fixtures))
	for name, fixture := range fixtures {
		approved["vitess-development-"+name] = fixture.Destination
	}
	return func(_ context.Context, project, environment string, spec database.Spec) (DatabasePolicy, error) {
		if project != "demo" || environment != "development" || spec.Engine != "vitess" || spec.Version != "23" || spec.Vitess == nil || !validVitessDedicatedFixtureNodes(spec.Placement.NodeNames) {
			return DatabasePolicy{}, fmt.Errorf("Vitess native fixture placement is not approved")
		}
		destination, ok := approved[spec.Name]
		if !ok || destination.ID == "" || spec.Vitess.BackupDestinationID != destination.ID || spec.Vitess.BackupDestinationRevision != destination.Revision {
			return DatabasePolicy{}, fmt.Errorf("Vitess native fixture destination is not approved")
		}
		return DatabasePolicy{NodeNames: append([]string(nil), vitessDedicatedFixtureNodes...), Pool: "vitess-acceptance", RuntimeClass: "runsc", StorageClass: "local-path"}, nil
	}
}

func selectVitessNativeFixturePolicy(nodes string) (bool, error) {
	if nodes == "" {
		return false, nil
	}
	selected := strings.Split(nodes, ",")
	if validVitessDedicatedFixtureNodes(selected) {
		return true, nil
	}
	for _, candidate := range selected {
		for _, dedicated := range vitessDedicatedFixtureNodes {
			if candidate == dedicated {
				return false, fmt.Errorf("dedicated Vitess fixture nodes require the exact approved triple")
			}
		}
	}
	return false, nil
}

func newVitessLiveClient(t *testing.T, duration time.Duration) (*vitessLiveFixtures, context.Context) {
	t.Helper()
	if os.Getenv("HAKOPOD_DATABASE_VITESS_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_VITESS_TEST=1 for named development cluster acceptance")
	}
	c, ctx := liveRecoveryClient(t, duration)
	file, err := os.Open(os.Getenv("HAKOPOD_VITESS_NATIVE_FIXTURE_CONFIG"))
	if err != nil {
		t.Fatal("protected Vitess native-storage fixture file is required")
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 || stat.Size() > 64<<10 {
		t.Fatal("Vitess fixture file must be a protected bounded regular file")
	}
	var config struct {
		Fixtures map[string]vitessLiveStorage `json:"fixtures"`
	}
	decoder := json.NewDecoder(io.LimitReader(file, (64<<10)+1))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || len(config.Fixtures) < 1 || len(config.Fixtures) > 8 {
		t.Fatal("invalid Vitess native-storage fixture configuration")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		t.Fatal("Vitess fixture file contains trailing data")
	}
	dedicatedPolicy, err := selectVitessNativeFixturePolicy(os.Getenv("HAKOPOD_DATABASE_FIXTURE_NODES"))
	if err != nil {
		t.Fatal(err)
	}
	if vitessNativeAcceptance && dedicatedPolicy {
		c.options.DatabasePolicy = vitessNativeFixtureDatabasePolicy(config.Fixtures)
	}
	byID := map[string]vitessLiveStorage{}
	keys := map[string]bool{}
	runID := make([]byte, 8)
	if _, err := rand.Read(runID); err != nil {
		t.Fatal(err)
	}
	for name, entry := range config.Fixtures {
		id, err := hex.DecodeString(entry.DatabaseID)
		if err != nil || len(id) != 16 || !entry.Dedicated || entry.Credentials.EncryptionIdentity != "" || keys[entry.Credentials.AccessKeyID] {
			t.Fatal("Vitess fixtures require separate approved identities and S3 credentials")
		}
		if _, found := byID[entry.DatabaseID]; found {
			t.Fatal("Vitess fixture identity is duplicated")
		}
		// Keep each rerun outside prior native backups retained in the same
		// dedicated bucket; a fresh namespace must initialize fresh MySQL data.
		entry.Destination.Prefix += "/run-" + hex.EncodeToString(runID)
		config.Fixtures[name] = entry
		keys[entry.Credentials.AccessKeyID] = true
		byID[entry.DatabaseID] = entry
	}
	revoked := map[string]bool{}
	if err := c.SetVitessBackupResolver(func(ctx context.Context, d database.Resource) (VitessBackupStorage, error) {
		entry, ok := byID[d.ID]
		if ctx.Err() != nil || !ok || revoked[d.ID] {
			return VitessBackupStorage{}, fmt.Errorf("fixture native storage was not approved")
		}
		return VitessBackupStorage{DatabaseID: entry.DatabaseID, Dedicated: entry.Dedicated, Destination: entry.Destination, Credentials: entry.Credentials, ApprovedEndpointCIDRs: entry.ApprovedEndpointCIDRs}, nil
	}); err != nil {
		t.Fatal(err)
	}
	return &vitessLiveFixtures{c: c, storage: config.Fixtures, revoked: revoked}, ctx
}

func newVitessFixture(t *testing.T, ctx context.Context, fixtures *vitessLiveFixtures, name string, shards int, replicas ...int) (database.Resource, []byte) {
	t.Helper()
	entry, ok := fixtures.storage[name]
	if !ok {
		t.Fatalf("native storage fixture %q is not configured", name)
	}
	d := database.Resource{ID: entry.DatabaseID, Project: "demo", Environment: "development", Revision: 1, Spec: database.Spec{SchemaVersion: 1, Name: "vitess-development-" + name, Engine: "vitess", Version: "23", Mode: "standalone", Shards: shards, CPU: "500m", Memory: "1Gi", StorageGiB: 1, TLS: &database.TLSConfig{Mode: "required"}, Vitess: &database.VitessConfig{BackupDestinationID: entry.Destination.ID, BackupDestinationRevision: entry.Destination.Revision}}}
	if shards > 1 {
		d.Spec.Mode, d.Spec.Replicas = "cluster", 1
		d.Spec.Vitess.Tables = []database.VitessTable{{Name: "records", ShardingColumn: "id"}}
	}
	if len(replicas) > 0 && replicas[0] > 0 {
		d.Spec.Mode, d.Spec.Replicas = "cluster", replicas[0]
	}
	d.Spec.Placement.NodeNames = []string{"k3d-hakopod-dev-server-0", "k3d-hakopod-database-worker-0"}
	if nodes := os.Getenv("HAKOPOD_DATABASE_FIXTURE_NODES"); nodes != "" {
		d.Spec.Placement.NodeNames = strings.Split(nodes, ",")
	}
	if len(d.Spec.Placement.NodeNames) < 2 || len(d.Spec.Placement.NodeNames) > 3 {
		t.Fatal("Vitess native fixtures require two or three named development nodes")
	}
	if !validVitessFixtureNodes(d.Spec.Placement.NodeNames) {
		t.Fatal("Vitess native fixtures require unique dedicated database development nodes")
	}
	if err := d.Spec.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := fixtures.c.vitessBackupStorage(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := fixtures.c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("Vitess fixture namespace already exists or its absence could not be verified")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	password := []byte(hex.EncodeToString(secret))
	t.Log("Development Vitess namespace", DatabaseNamespace(d.ID))
	t.Cleanup(func() {
		if t.Failed() && os.Getenv("HAKOPOD_KEEP_DATABASE_FIXTURES") == "1" {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		for cleanup.Err() == nil {
			done, err := fixtures.c.DeleteDatabase(cleanup, d, func() error { return cleanup.Err() })
			if err != nil {
				t.Error("Vitess fixture deletion failed", err)
				return
			}
			if done {
				return
			}
			if sleepContext(cleanup, 2*time.Second) != nil {
				break
			}
		}
		t.Error("Vitess fixture did not finish reclaiming its owned resources")
	})
	return d, password
}

func validVitessFixtureNodes(nodes []string) bool {
	legacy := map[string]bool{"k3d-hakopod-dev-server-0": true, "k3d-hakopod-database-worker-0": true, "k3d-hakopod-database-worker-1": true}
	seen := map[string]bool{}
	for _, node := range nodes {
		if node == "" || seen[node] {
			return false
		}
		seen[node] = true
	}
	if len(nodes) >= 2 && len(nodes) <= 3 {
		allLegacy := true
		for _, node := range nodes {
			allLegacy = allLegacy && legacy[node]
		}
		if allLegacy {
			return true
		}
	}
	return validVitessDedicatedFixtureNodes(nodes)
}

func validVitessDedicatedFixtureNodes(nodes []string) bool {
	if len(nodes) != len(vitessDedicatedFixtureNodes) {
		return false
	}
	seen := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		if node == "" || seen[node] {
			return false
		}
		seen[node] = true
	}
	for _, node := range vitessDedicatedFixtureNodes {
		if !seen[node] {
			return false
		}
	}
	return true
}

func waitVitessFixture(t *testing.T, ctx context.Context, c *Client, d database.Resource, password []byte) database.Observation {
	t.Helper()
	var health database.Observation
	var last error
	for ctx.Err() == nil {
		step, cancel := context.WithTimeout(ctx, 30*time.Second)
		last = c.ApplyDatabase(step, d, password, func() error { return step.Err() })
		if last == nil {
			health, last = c.ObserveDatabase(step, d)
		}
		cancel()
		if last == nil && health.Status == "ready" {
			return health
		}
		t.Log("Waiting for Vitess", health.Message, last)
		if sleepContext(ctx, 5*time.Second) != nil {
			break
		}
	}
	t.Fatal("Vitess did not become ready", health.Message, last)
	return health
}

func vitessFixtureClient(t *testing.T, ctx context.Context, c *Client, d database.Resource, health database.Observation, password []byte, route string, gateway int) *sql.DB {
	t.Helper()
	if health.Routing == nil || gateway >= len(health.Routing.Members) {
		t.Fatal("Vitess gateway is unavailable")
	}
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	config, err := redisTLSConfig(trust, "database."+DatabaseNamespace(d.ID)+".svc")
	if err != nil {
		t.Fatal(err)
	}
	client, err := c.vitessGatewayClientWithReadTimeout(ctx, d, health.Routing.Members[gateway], route, password, config, 35*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func seedVitessFixture(t *testing.T, ctx context.Context, client *sql.DB) {
	t.Helper()
	if _, err := client.ExecContext(ctx, "CREATE TABLE records (id BIGINT NOT NULL PRIMARY KEY, payload VARBINARY(16) NOT NULL, label VARCHAR(80) CHARACTER SET utf8mb4 NOT NULL)"); err != nil {
		t.Fatal("Vitess schema creation failed", err)
	}
	for i := 1; i <= 32; i++ {
		if _, err := client.ExecContext(ctx, "INSERT INTO records(id,payload,label) VALUES (?,?,?)", i, []byte{0, 10, 255, byte(i)}, "नमस्ते / 東京"); err != nil {
			t.Fatal("Vitess application write failed", err)
		}
	}
}

func checkVitessFixtureData(t *testing.T, ctx context.Context, client *sql.DB) {
	t.Helper()
	var count int
	if err := client.QueryRowContext(ctx, "SELECT COUNT(*) FROM records").Scan(&count); err != nil || count != 32 {
		t.Fatal("Vitess scatter read changed the record count", err)
	}
	for i := 1; i <= 32; i++ {
		var payload []byte
		var label string
		if err := client.QueryRowContext(ctx, "SELECT payload,label FROM records WHERE id=?", i).Scan(&payload, &label); err != nil || !bytes.Equal(payload, []byte{0, 10, 255, byte(i)}) || label != "नमस्ते / 東京" {
			t.Fatal("Vitess routed read changed binary or Unicode data", err)
		}
	}
}

func checkVitessShardRouting(t *testing.T, ctx context.Context, c *Client, d database.Resource, health database.Observation) {
	t.Helper()
	primaries, err := vitessObservedPrimaries(d, health)
	if err != nil {
		t.Fatal(err)
	}
	// Vitess's documented hash vindex encrypts an unsigned big-endian integer
	// with a zero-key Triple DES block; the high byte chooses this shard range.
	hash, err := des.NewTripleDESCipher(make([]byte, 24))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{}
	for i := uint64(1); i <= 32; i++ {
		var input, output [8]byte
		binary.BigEndian.PutUint64(input[:], i)
		hash.Encrypt(output[:], input[:])
		shard := "-"
		if d.Spec.Shards == 2 {
			if output[0] < 128 {
				shard = "-80"
			} else {
				shard = "80-"
			}
		}
		want[shard] = append(want[shard], strconv.FormatUint(i, 10))
	}
	for _, member := range primaries {
		out := &databaseBoundedWriter{limit: 1024}
		if err := c.DatabaseExec(ctx, d, member, vitessLocalCommand("vt_dba", "SELECT id FROM records ORDER BY id"), nil, out); err != nil {
			t.Fatal(err)
		}
		got := strings.Fields(out.String())
		sort.Strings(got)
		sort.Strings(want[member.Shard])
		if strings.Join(got, ",") != strings.Join(want[member.Shard], ",") {
			t.Fatal("Vitess placed rows on the wrong physical shard", member.Shard)
		}
	}
}

func checkVitessTabletData(t *testing.T, ctx context.Context, c *Client, d database.Resource, member database.Member, marker bool) {
	t.Helper()
	hash, err := des.NewTripleDESCipher(make([]byte, 24))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{}
	for i := uint64(1); i <= 32; i++ {
		var input, output [8]byte
		binary.BigEndian.PutUint64(input[:], i)
		hash.Encrypt(output[:], input[:])
		shard := "-"
		if d.Spec.Shards == 2 {
			if output[0] < 128 {
				shard = "-80"
			} else {
				shard = "80-"
			}
		}
		if shard == member.Shard {
			want = append(want, fmt.Sprintf("%d\t%s\t%s", i, strings.ToUpper(hex.EncodeToString([]byte{0, 10, 255, byte(i)})), strings.ToUpper(hex.EncodeToString([]byte("नमस्ते / 東京")))))
		}
	}
	if marker {
		want = append(want, "99\tCAFE\t"+strings.ToUpper(hex.EncodeToString([]byte("after-replica-stopped"))))
	}
	wait, stop := context.WithTimeout(ctx, time.Minute)
	defer stop()
	for {
		out := &databaseBoundedWriter{limit: 16 << 10}
		if err := c.DatabaseExec(wait, d, member, vitessLocalCommand("vt_app", "SELECT id,HEX(payload),HEX(label) FROM records ORDER BY id"), nil, out); err != nil {
			t.Fatal("Vitess local application query failed", err)
		}
		if strings.TrimSpace(out.String()) == strings.Join(want, "\n") {
			return
		}
		if sleepContext(wait, time.Second) != nil {
			t.Fatal("Vitess physical tablet lost or changed seeded binary or Unicode rows", member.Shard)
		}
	}
}

func TestManagedVitessLive(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		shards int
	}{{"standalone", 1}, {"cluster", 2}} {
		t.Run(scenario.name, func(t *testing.T) {
			fixtures, ctx := newVitessLiveClient(t, 25*time.Minute)
			d, password := newVitessFixture(t, ctx, fixtures, scenario.name, scenario.shards)
			c := fixtures.c
			health := waitVitessFixture(t, ctx, c, d, password)
			if health.TLS == nil || !health.TLS.Verified || !health.TLS.PlaintextRejected || health.Routing == nil || !health.Routing.Ready || health.Coordination == nil || !health.Coordination.Ready {
				t.Fatal("Vitess transport, topology and gateway readiness were not verified")
			}
			client := vitessFixtureClient(t, ctx, c, d, health, password, "app@primary", 0)
			seedVitessFixture(t, ctx, client)
			for i := range health.Routing.Members {
				checkVitessFixtureData(t, ctx, vitessFixtureClient(t, ctx, c, d, health, password, "app@primary", i))
			}
			checkVitessShardRouting(t, ctx, c, d, health)
			testVitessSecurity(t, ctx, c, d, health, password)
			if d.Spec.Mode == "cluster" {
				health = testVitessPrimaryLoss(t, ctx, c, d, health, password)
			}
			testVitessRenewal(t, ctx, c, d, health, password)
		})
	}
}

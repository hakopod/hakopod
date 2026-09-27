package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/store"
)

func TestLiveDockerDatabaseImports(t *testing.T) {
	if os.Getenv("HAKOPOD_MANAGED_DATABASE_BACKUP_TEST") != "1" {
		t.Skip("requires disposable Docker PostgreSQL and S3 fixtures")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	db, dsn := database(t)
	token, err := db.Bootstrap(ctx, "docker-import-development-fixture")
	if err != nil {
		t.Fatal(err)
	}
	endpoint, access, secret, _ := liveBackupObjectStore(t, ctx)
	state := t.TempDir()
	if err = os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	server := &api.Server{Store: db, Auth: api.AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{23}, 32))}}
	server.ConfigureBackups(api.BackupConfig{DatabaseURL: dsn, StateDir: state, MaxBytes: 16 << 20})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	client := backupRequestClient{t, httpServer, token}
	var destination struct {
		Destination backup.Destination `json:"destination"`
	}
	if status := client.request("POST", "/backup-destinations", backup.DestinationInput{Name: "docker-import-development-fixture", Endpoint: endpoint, Region: "us-east-1", Bucket: "hakopod-backup-tests", Prefix: "imports", PathStyle: true, AllowHTTP: true, AccessKeyID: access, SecretAccessKey: secret}, &destination, ""); status != 201 {
		t.Fatal("destination", status)
	}
	name := "hakopod-import-development-" + store.NewID()[:10]
	if err = exec.CommandContext(ctx, "docker", "run", "--rm", "-d", "--name", name, "--label", "com.hakopod.test=database-imports", "--memory=192m", "--cpus=0.5", "--pids-limit=128", "-e", "POSTGRES_PASSWORD="+store.NewID(), "-e", "POSTGRES_USER=app", "-e", "POSTGRES_DB=app", liveBackupPostgres, "-c", "shared_buffers=16MB", "-c", "max_connections=12").Run(); err != nil {
		t.Fatal("start Docker source", err)
	}
	defer func() {
		clean, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		_ = exec.CommandContext(clean, "docker", "rm", "--force", "--volumes", name).Run()
	}()
	for {
		if exec.CommandContext(ctx, "docker", "exec", name, "pg_isready", "-U", "app").Run() == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("Docker source not ready")
		case <-time.After(time.Second):
		}
	}
	sql := func(query string) string {
		t.Helper()
		out, e := exec.CommandContext(ctx, "docker", "exec", name, "psql", "-XAt", "-U", "app", "-d", "app", "-v", "ON_ERROR_STOP=1", "-c", query).Output()
		if e != nil {
			t.Fatal("Docker source SQL", e)
		}
		return string(out)
	}
	sql("CREATE TABLE import_rows(id integer PRIMARY KEY); INSERT INTO import_rows VALUES(1)")
	captured := time.Now().UTC().Truncate(time.Microsecond)
	raw, err := exec.CommandContext(ctx, "docker", "exec", name, "pg_dump", "-Fc", "-U", "app", "-d", "app").Output()
	if err != nil {
		t.Fatal("capture Docker archive", err)
	}
	sum := sha256.Sum256(raw)
	spec := backup.ImportSpec{DestinationID: destination.Destination.ID, SourceName: "docker-import-development-fixture", Engine: "postgresql", SourceVersion: "17", CapturedAt: captured, Bytes: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])}
	var review backup.Import
	if status := client.request("POST", "/backup-imports", spec, &review, store.NewID()); status != 201 {
		t.Fatal("import review", status)
	}
	upload := func(id string, payload []byte) (int, backup.Artifact) {
		t.Helper()
		request, e := http.NewRequestWithContext(ctx, "PUT", httpServer.URL+"/api/v1/backup-imports/"+id+"/archive", bytes.NewReader(payload))
		if e != nil {
			t.Fatal(e)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/octet-stream")
		response, e := httpServer.Client().Do(request)
		if e != nil {
			t.Fatal("upload request", e)
		}
		defer response.Body.Close()
		var a backup.Artifact
		if response.StatusCode < 300 {
			if e = json.NewDecoder(response.Body).Decode(&a); e != nil {
				t.Fatal(e)
			}
		}
		return response.StatusCode, a
	}
	bad := append([]byte(nil), raw...)
	bad[len(bad)-1] ^= 1
	if status, _ := upload(review.ID, bad); status != 400 {
		t.Fatal("checksum mismatch response", status)
	}
	status, a := upload(review.ID, raw)
	if status != 201 || a.VerifiedAt == nil || a.SourceVersion != "17" || a.Source.Kind != "docker_import" {
		t.Fatal("verified Docker import", status)
	}
	if status, replay := upload(review.ID, raw); status != 200 || replay.ID != a.ID {
		t.Fatal("completed upload replay", status)
	}
	spec.SourceVersion = "18"
	if status := client.request("POST", "/backup-imports", spec, &review, store.NewID()); status != 201 {
		t.Fatal("wrong-version review", status)
	}
	if status, _ := upload(review.ID, raw); status != 400 {
		t.Fatal("wrong major accepted", status)
	}
	sql("INSERT INTO import_rows VALUES(2)")
	if sql("SELECT count(*) FROM import_rows") != "2\n" {
		t.Fatal("Docker source changed")
	}
	t.Log("real Docker PostgreSQL dump, raw checksum and version rejection, encrypted S3 readback, retry and untouched writable Docker source passed")
	redisName := "hakopod-import-redis-development-" + store.NewID()[:10]
	redisImage := "quay.io/opstree/redis:v8.2.1@sha256:8027cf7ec625d625a4f60e2526f0073d2b58bf6a6172992015f7699ecbd8504a"
	if err = exec.CommandContext(ctx, "docker", "run", "--rm", "-d", "--name", redisName, "--label", "com.hakopod.test=database-imports", "--memory=96m", "--cpus=0.5", "--tmpfs", "/data:uid=1000,gid=1000", redisImage, "redis-server", "--dir", "/data", "--appendonly", "no").Run(); err != nil {
		t.Fatal("start Docker Redis source", err)
	}
	defer func() {
		clean, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		_ = exec.CommandContext(clean, "docker", "rm", "--force", "--volumes", redisName).Run()
	}()
	for {
		if exec.CommandContext(ctx, "docker", "exec", redisName, "redis-cli", "PING").Run() == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("Redis source not ready")
		case <-time.After(time.Second):
		}
	}
	for _, command := range [][]string{{"SET", "import-value", "preserved", "EX", "3600"}, {"SAVE"}} {
		args := append([]string{"exec", redisName, "redis-cli"}, command...)
		if err = exec.CommandContext(ctx, "docker", args...).Run(); err != nil {
			t.Fatal("capture Redis archive", err)
		}
	}
	raw, err = exec.CommandContext(ctx, "docker", "exec", redisName, "cat", "/data/dump.rdb").Output()
	if err != nil {
		t.Fatal("read RDB", err)
	}
	sum = sha256.Sum256(raw)
	spec.SourceName, spec.Engine, spec.SourceVersion = "docker-redis-development-fixture", "redis", "8"
	spec.Bytes, spec.SHA256, spec.CapturedAt = int64(len(raw)), hex.EncodeToString(sum[:]), time.Now().UTC().Truncate(time.Microsecond)
	if status := client.request("POST", "/backup-imports", spec, &review, store.NewID()); status != 201 {
		t.Fatal("Redis import review", status)
	}
	status, a = upload(review.ID, raw)
	if status != 201 || a.VerifiedAt == nil || a.SourceVersion != "8" || a.Format != "age-v1+redis-shards-v1" {
		t.Fatal("verified Redis import", status)
	}
	out, err := exec.CommandContext(ctx, "docker", "exec", redisName, "redis-cli", "GET", "import-value").Output()
	if err != nil || string(out) != "preserved\n" {
		t.Fatal("Redis source changed", err)
	}
	t.Log("real Docker Redis 8 RDB version metadata and encrypted S3 import passed")

}

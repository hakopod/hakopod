package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
	"k8s.io/client-go/tools/clientcmd"
)

// This test seeds failures only in its disposable control-plane datastore.
// Controller status, database membership and SQL results are always native.
func TestManagedMySQLResizeRetryLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_MYSQL_RETRY_TEST") != "1" {
		t.Skip("requires the named development cluster and disposable control-plane PostgreSQL")
	}
	if os.Getenv("HAKOPOD_TEST_DATABASE_URL") == "" {
		t.Fatal("native retry acceptance requires disposable control-plane PostgreSQL")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("native retry acceptance requires k3d-hakopod-dev")
	}
	c, err := cluster.New(path, cluster.Options{})
	if err != nil {
		t.Fatal("open development cluster", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	s := notificationTestDB(t)
	token, err := s.Bootstrap(ctx, "mysql-retry-native-fixture")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	key, randomPassword := make([]byte, 32), make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		t.Fatal(err)
	}
	if _, err = rand.Read(randomPassword); err != nil {
		t.Fatal(err)
	}
	password := []byte(hex.EncodeToString(randomPassword))
	d := database.Resource{ID: store.NewID(), Project: "demo", Environment: "development", Spec: database.Spec{
		SchemaVersion: 1, Name: "mysql-retry-native-fixture", Engine: "mysql", Version: "8.4",
		Mode: "cluster", Replicas: 2, Shards: 1, CPU: "1", Memory: "1Gi", StorageGiB: 1,
	}}
	d.Spec = d.Spec.WithSecureDefaults()
	if value := os.Getenv("HAKOPOD_DATABASE_FIXTURE_NODES"); value != "" {
		seen := map[string]bool{}
		for _, node := range strings.Split(value, ",") {
			if (node != "k3d-hakopod-dev-server-0" && node != "k3d-hakopod-database-worker-0") || seen[node] {
				t.Fatal("retry fixture requires the dedicated database development nodes")
			}
			seen[node] = true
			d.Spec.Placement.NodeNames = append(d.Spec.Placement.NodeNames, node)
		}
	}
	d.EncryptedCredentials, err = database.SealCredentials(key, d.ID, password)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: s, Cluster: c, Auth: AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(key)}}
	handler := server.Handler()
	t.Log("Development MySQL retry namespace", cluster.DatabaseNamespace(d.ID))
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 8*time.Minute)
		defer stop()
		for cleanup.Err() == nil {
			done, e := c.DeleteDatabase(cleanup, d, func() error { return cleanup.Err() })
			if e != nil {
				t.Error("retry fixture cleanup failed", e)
				return
			}
			if done {
				t.Log("Native retry fixture namespace and volumes reclaimed")
				return
			}
			select {
			case <-cleanup.Done():
			case <-time.After(2 * time.Second):
			}
		}
		t.Error("retry fixture cleanup did not finish")
	})
	request := func(action string, body any, key string, expected int, out any) {
		t.Helper()
		encoded, e := json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/databases/"+d.ID+"/"+action, bytes.NewReader(encoded)).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer "+token)
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != expected {
			// Responses may contain native errors; do not print bodies or credentials.
			t.Fatalf("%s returned HTTP %d, expected %d", action, w.Code, expected)
		}
		if e = json.Unmarshal(w.Body.Bytes(), out); e != nil {
			t.Fatal("decode native retry response", e)
		}
	}
	load := func() database.Resource {
		t.Helper()
		current, e := s.Database(ctx, p, d.ID, true)
		if e != nil {
			t.Fatal(e)
		}
		return current
	}
	wait := func(operationID string, revision int64, members int) database.Resource {
		t.Helper()
		for ctx.Err() == nil {
			server.reconcileDatabase(ctx)
			op, e := s.DatabaseOperation(ctx, p, operationID)
			if e != nil {
				t.Fatal(e)
			}
			if op.Status == "failed" || op.Status == "cancelled" {
				t.Fatalf("native operation %s ended %s in phase %s", operationID, op.Status, op.Phase)
			}
			if op.Status == "succeeded" {
				current := load()
				o := current.Observation
				if current.Revision != revision || current.Status != "ready" || o.Revision != revision || o.Status != "ready" || len(o.Members) != members || o.TLS == nil || !o.TLS.Verified || !o.TLS.PlaintextRejected || o.Routing == nil || !o.Routing.Ready {
					t.Fatal("retry completed without the exact revision, member inventory or native TLS/routing checks")
				}
				return current
			}
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
			}
		}
		t.Fatal("native resize retry exceeded its bounded acceptance window")
		return database.Resource{}
	}
	created, err := s.AcceptDatabase(ctx, p, d, 0, "native-retry-create", "create")
	if err != nil {
		t.Fatal(err)
	}
	d = wait(created.ID, 1, 3)
	query := func(statement string) string {
		t.Helper()
		trust, e := c.DatabaseTrust(ctx, d)
		if e != nil {
			t.Fatal("read public database trust", e)
		}
		var endpoint database.Endpoint
		for _, candidate := range d.Observation.Endpoints {
			if candidate.Purpose == "read_write" {
				endpoint = candidate
			}
		}
		if endpoint.Host == "" {
			t.Fatal("native write endpoint is missing")
		}
		input := bytes.Join([][]byte{password, []byte(trust.CertificatePEM)}, []byte{'\n'})
		script := `set -eu; IFS= read -r MYSQL_PWD; export MYSQL_PWD; umask 077; work=$(mktemp -d); trap 'rm -rf "$work"' EXIT; cat > "$work/ca.crt"; mysql --no-defaults --protocol=TCP --host="$1" --port="$2" --user=app --database=app --connect-timeout=3 --ssl-mode=VERIFY_IDENTITY --ssl-ca="$work/ca.crt" --batch --raw --skip-column-names --execute="$3"`
		out := &mysqlRetryOutput{remaining: 4096}
		if e = c.DatabaseExec(ctx, d, d.Observation.Members[0], []string{"sh", "-c", script, "mysql-retry-query", endpoint.Host, strconv.Itoa(endpoint.Port), statement}, bytes.NewReader(input), out); e != nil {
			t.Fatal("native retry SQL check failed")
		}
		return strings.TrimSpace(out.String())
	}
	query("CREATE TABLE retry_data(id INT PRIMARY KEY, value VARBINARY(16)); INSERT INTO retry_data VALUES (1, 0x000AFF80)")
	for _, test := range []struct {
		state    string
		replicas int
	}{{"prior", 4}, {"accepted", 2}} {
		prior := d
		next := d.Spec
		next.Replicas = test.replicas
		var review struct {
			ID   string              `json:"id"`
			Plan database.ResizePlan `json:"plan"`
		}
		request("resize-plan", map[string]any{"spec": next}, "", http.StatusOK, &review)
		if len(review.Plan.BlockedReasons) != 0 {
			t.Fatal("native resize plan is blocked")
		}
		var resize database.Operation
		request("resize", map[string]any{"review_id": review.ID, "expected_revision": d.Revision, "spec": next}, store.NewID(), http.StatusAccepted, &resize)
		d = load()
		if d.Revision != prior.Revision+1 {
			t.Fatal("original resize did not accept exactly one desired revision")
		}
		claim, e := s.ClaimDatabaseOperation(ctx)
		if e != nil || claim.ID != resize.ID {
			t.Fatal("could not claim the fixture resize")
		}
		phase := "review"
		if test.state == "accepted" {
			// Apply the actual revision, then seed only a control-plane timeout.
			step, stop := context.WithTimeout(ctx, 25*time.Second)
			e = c.ApplyDatabase(step, d, password, func() error { return s.CheckDatabaseOperation(step, claim) })
			stop()
			if e != nil {
				t.Fatal("native resize application failed", e)
			}
			phase = "timeout"
		}
		applied, e := c.DatabaseRevisionApplied(ctx, d)
		if e != nil || applied != (test.state == "accepted") {
			t.Fatal("native controller revision did not match the intended retry state")
		}
		if e = s.RecordDatabaseStep(ctx, claim, prior.Observation, "failed", phase, "Deliberate development fixture failure for resize retry acceptance."); e != nil {
			t.Fatal(e)
		}
		var retry struct {
			ID   string                     `json:"id"`
			Plan database.ResizeRetryReview `json:"plan"`
		}
		request("resize-retry-plan", map[string]any{"operation_id": resize.ID, "expected_revision": d.Revision}, "", http.StatusOK, &retry)
		if retry.Plan.State != test.state || retry.Plan.OperationID != resize.ID || retry.Plan.Revision != d.Revision || !retry.Plan.Resize.Proposed.Equal(d.Spec) {
			t.Fatal("fresh native review did not preserve the requested replica change")
		}
		body := map[string]any{"operation_id": resize.ID, "expected_revision": d.Revision, "review_id": retry.ID, "confirm_name": d.Spec.Name}
		requestKey := store.NewID()
		var attempt database.Operation
		request("resize-retry", body, requestKey, http.StatusAccepted, &attempt)
		if attempt.ID == resize.ID || attempt.Kind != "resize-retry" || attempt.Revision != d.Revision {
			t.Fatal("retry did not create a distinct attempt at the accepted revision")
		}
		d = wait(attempt.ID, d.Revision, test.replicas+1)
		if got := query("SELECT HEX(value) FROM retry_data WHERE id=1"); got != "000AFF80" {
			t.Fatal("native replica retry did not preserve the committed binary value")
		}
		var replay database.Operation
		request("resize-retry", body, requestKey, http.StatusAccepted, &replay)
		if replay.ID != attempt.ID || replay.Status != "succeeded" || load().Revision != d.Revision {
			t.Fatal("completed retry was replayed as a new operation or revision")
		}
		original, e := s.DatabaseOperation(ctx, p, resize.ID)
		if e != nil || original.Status != "failed" || original.Phase != phase {
			t.Fatal("retry overwrote the original failure record")
		}
		t.Logf("Native %s-state retry passed: revision %d, %d members, retained data, original failure and exact replay", test.state, d.Revision, len(d.Observation.Members))
	}
}

type mysqlRetryOutput struct {
	strings.Builder
	remaining int
}

func (w *mysqlRetryOutput) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		return 0, fmt.Errorf("native retry output exceeds bound: %w", io.ErrShortBuffer)
	}
	w.remaining -= len(p)
	return w.Builder.Write(p)
}

package cluster

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
)

const xemFixtureLabel = "hakopod.io/xem-acceptance"
const xemFixtureHostname = "xem.example.test"
const xemFixtureHost = xemFixtureHostname + ":8443"

// This opt-in suite uses a separately reserved worker in the named development
// cluster. Candidate images must be actual registry manifest references. The
// private HTTP S3 fixture and same-namespace external dependencies are explicit
// development fixtures, not claims about public TLS or provider networking.
func TestLiveXemTemplate(t *testing.T) {
	if os.Getenv("HAKOPOD_XEM_TEST") != "1" {
		t.Skip("set HAKOPOD_XEM_TEST=1 for Xem runtime acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires explicit k3d-hakopod-dev kubeconfig")
	}
	nodeName, pool, nodeUID := os.Getenv("HAKOPOD_XEM_TEST_NODE"), os.Getenv("HAKOPOD_XEM_TEST_POOL"), os.Getenv("HAKOPOD_XEM_TEST_NODE_UID")
	if nodeName == "" || pool == "" || nodeUID == "" {
		t.Fatal("requires an explicitly owned development worker, pool and observed node UID")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 38*time.Minute)
	defer cancel()
	c, err := New(path, Options{AppDomain: "127.0.0.1.sslip.io", RolloutTimeout: 6 * time.Minute, WorkloadPolicy: func(context.Context, string, string, spec.Application) (WorkloadPolicy, error) {
		return WorkloadPolicy{NodeName: nodeName, Pool: pool, Recreate: true}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	node, err := c.kube.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil || string(node.UID) != nodeUID || node.Labels[xemFixtureLabel] != pool || node.Labels["hakopod.com/pool"] != pool {
		t.Fatal("development worker ownership does not match", err)
	}
	for _, modes := range []struct{ database, redis, storage string }{{"bundled", "bundled", "bundled"}, {"external", "bundled", "bundled"}, {"bundled", "external", "bundled"}, {"external", "external", "bundled"}, {"external", "external", "external"}} {
		if !t.Run(modes.database+"-postgres_"+modes.redis+"-redis_"+modes.storage+"-storage", func(t *testing.T) { xemAcceptance(t, ctx, c, path, node, modes.database, modes.redis, modes.storage) }) {
			break
		}
	}
}

func xemAcceptance(t *testing.T, ctx context.Context, c *Client, path string, node *corev1.Node, databaseMode, redisMode, storageMode string) {
	t.Helper()
	runID := fmt.Sprintf("xem-%d", time.Now().UnixNano())
	app := xemCandidate(t, runID, node.Status.NodeInfo.Architecture, databaseMode, redisMode, storageMode)
	mathesarCapacity(t, ctx, c, node, app)
	target := Target{ApplicationID: runID, Project: "xem-acceptance", Environment: "test", OperationID: "fixtures", Revision: 1, Spec: app}
	labels := labelsFor(target, "")
	labels[xemFixtureLabel] = runID
	ns, err := c.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(runID), Labels: labels}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secrets := xemSecrets(t, app)
	defer xemCleanup(t, c, ns, target, secrets)
	for ref, value := range secrets {
		if err := c.CreateWorkloadSecret(ctx, target.Project, target.Environment, app.Name, ref, value); err != nil {
			t.Fatal(err)
		}
	}
	deploy := func() {
		t.Helper()
		if _, err := c.Deploy(ctx, target, func(e Event) { t.Log(e.Type, e.Message) }); err != nil {
			t.Fatal("Xem deployment", err)
		}
	}
	target.Spec.Services = map[string]spec.Service{}
	for name, service := range app.Services {
		if name != "backend" && name != "frontend" && name != "main" {
			target.Spec.Services[name] = service
		}
	}
	deploy()
	storageService, storageAccess, storageSecret, bucket := "storage", secrets["storage-user"], secrets["storage-password"], "xem-files"
	if storageMode == "external" {
		storageService = "fixture-storage"
		storageAccess = secrets["storage-access-key"]
		storageSecret = secrets["storage-secret-key"]
		bucket = "acceptance-private"
	}
	storageAddress, storageStop := xemPortForward(t, ctx, path, ns.Name, storageService, 9000)
	defer storageStop()
	store := s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String("http://" + storageAddress), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider(storageAccess, storageSecret, ""), HTTPClient: &http.Client{Timeout: 20 * time.Second}})
	if storageMode == "external" {
		if _, err := store.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &bucket}); err != nil {
			t.Fatal("create private external fixture bucket", err)
		}
	} else {
		buckets, err := store.ListBuckets(ctx, &s3.ListBucketsInput{})
		if err != nil || len(buckets.Buckets) != 0 {
			t.Fatal("bundled MinIO must start without an existing bucket", err)
		}
		t.Log("bundled MinIO is empty; backend must create its private bucket")
	}
	target.Spec = app
	target.Revision++
	target.OperationID = "application"
	deploy()
	// A healthy HTTP process can exist even when upstream administrator seeding
	// failed. Keep this initial diagnostic bounded and free of secret values.
	podsForSeed, seedErr := c.kube.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{Limit: 30})
	if seedErr == nil {
		for _, pod := range podsForSeed.Items {
			if !strings.HasPrefix(pod.Name, "backend-") {
				continue
			}
			raw, e := c.kube.CoreV1().Pods(ns.Name).GetLogs(pod.Name, &corev1.PodLogOptions{LimitBytes: ptr(int64(128 << 10))}).DoRaw(ctx)
			if e != nil {
				continue
			}
			redacted := string(raw)
			for _, secret := range secrets {
				redacted = strings.ReplaceAll(redacted, secret, "[fixture secret]")
			}
			lines := 0
			for _, line := range strings.Split(redacted, "\n") {
				lower := strings.ToLower(line)
				if strings.Contains(lower, "super admin") || strings.Contains(lower, "failed to read file") || strings.Contains(lower, "failed to create team") {
					if len(line) > 1200 {
						line = line[:1200]
					}
					t.Log("administrator seed status", line)
					lines++
					if lines >= 8 {
						break
					}
				}
			}
		}
	}
	address, stop := xemPortForward(t, ctx, path, ns.Name, "main", 8080)
	defer stop()
	client := &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(method, route, contentType, token string, body []byte) (int, []byte, http.Header) {
		t.Helper()
		return xemRequest(t, ctx, client, address, method, route, contentType, token, body, nil)
	}
	status, _, _ := request("GET", "/health", "", "", nil)
	if status != 200 {
		t.Fatal("health status", status)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+address+"/api/auth/providers", nil)
	req.Host = "wrong.example.test"
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatal("proxy accepted an unexpected host", resp.StatusCode)
	}
	status, _, _ = request("GET", "/api/v1/users/me", "", "", nil)
	if status != 401 {
		t.Fatal("anonymous account access", status)
	}
	credentialsJSON, _ := json.Marshal(map[string]string{"email": "admin@example.test", "password": secrets["admin-password"]})
	status, data, _ := request("POST", "/api/v1/auth/login", "application/json", "", credentialsJSON)
	var login struct {
		Token   string `json:"token"`
		Refresh string `json:"refresh_token"`
	}
	if status != 200 || json.Unmarshal(data, &login) != nil || login.Token == "" || login.Refresh == "" {
		t.Fatal("admin login failed", status)
	}
	status, data, _ = request("GET", "/api/v1/users/me", "", login.Token, nil)
	var user struct {
		Email string `json:"email"`
		ID    string `json:"id"`
	}
	if status != 200 || json.Unmarshal(data, &user) != nil || user.Email != "admin@example.test" || user.ID == "" {
		t.Fatal("authenticated user lookup failed", status)
	}
	initialRefreshJSON, _ := json.Marshal(map[string]string{"refresh_token": login.Refresh})
	status, data, _ = request("POST", "/api/v1/auth/refresh", "application/json", "", initialRefreshJSON)
	var initialRefresh struct {
		Token string `json:"token"`
	}
	if status != 200 || json.Unmarshal(data, &initialRefresh) != nil || initialRefresh.Token == "" {
		t.Fatal("initial token refresh failed", status)
	}
	login.Token = initialRefresh.Token
	xemFrontendSession(t, ctx, client, address, secrets["admin-password"])
	payload := []byte("Real Xem acceptance file. Database, object and session persistence.\n")
	var upload bytes.Buffer
	writer := multipart.NewWriter(&upload)
	part, err := writer.CreateFormFile("file", "acceptance.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(payload)
	_ = writer.Close()
	status, data, _ = request("POST", "/api/v1/files/upload", writer.FormDataContentType(), login.Token, upload.Bytes())
	var file struct {
		ID string `json:"file"`
	}
	if status != 200 || json.Unmarshal(data, &file) != nil || file.ID == "" {
		t.Fatal("real S3 upload failed", status)
	}
	verifyFile := func() {
		t.Helper()
		status, data, _ = request("GET", "/api/v1/files/"+file.ID, "", login.Token, nil)
		if status != 200 {
			t.Fatal("stored file lookup failed", status)
		}
		var f struct {
			Path      string `json:"path"`
			SignedURL string `json:"signedUrl"`
		}
		if json.Unmarshal(data, &f) != nil || f.Path == "" || f.SignedURL == "" {
			t.Fatal("stored file lacks application-signed URL")
		}
		xemVerifyObject(t, ctx, client, store, storageAddress, address, storageService, storageMode, bucket, f.Path, f.SignedURL, payload)
	}
	verifyFile()
	if databaseMode == "bundled" && redisMode == "bundled" && storageMode == "bundled" {
		xemBrowserHandoff(t, ctx, path, ns.Name, address, secrets["admin-password"])
	}
	// Set a real durable Redis key, independently of HTTP readiness. Its database
	// index is deliberately nonzero for the external Redis mode.
	redisName := "redis"
	if redisMode == "external" {
		redisName = "fixture-redis"
	}
	redisDB := "0"
	if redisMode == "external" {
		redisDB = "2"
	}
	redisCommand := func(command string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", ns.Name, "exec", "deployment/"+redisName, "--", "sh", "-ec", `REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli -n `+redisDB+" "+command)
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatal("fixture Redis operation failed", e)
		}
		return strings.TrimSpace(string(out))
	}
	if redisCommand("SET acceptance:persistent verified") != "OK" {
		t.Fatal("Redis durable write failed")
	}
	if redisCommand("SAVE") != "OK" {
		t.Fatal("Redis persistence checkpoint failed")
	}
	claims, err := c.kube.CoreV1().PersistentVolumeClaims(ns.Name).List(ctx, metav1.ListOptions{Limit: 20})
	if err != nil || claims.Continue != "" {
		t.Fatal("list owned claims", err)
	}
	claimUIDs := map[string]types.UID{}
	for _, claim := range claims.Items {
		claimUIDs[claim.Name] = claim.UID
	}
	stop()
	for name, service := range target.Spec.Services {
		service.RestartNonce = "durability"
		target.Spec.Services[name] = service
	}
	target.Revision++
	target.OperationID = "restart"
	deploy()
	address, stop = xemPortForward(t, ctx, path, ns.Name, "main", 8080)
	defer stop()
	storageStop()
	storageAddress, storageStop = xemPortForward(t, ctx, path, ns.Name, storageService, 9000)
	client.CloseIdleConnections()
	defer storageStop()
	store = s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String("http://" + storageAddress), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider(storageAccess, storageSecret, ""), HTTPClient: &http.Client{Timeout: 20 * time.Second}})
	refreshJSON, _ := json.Marshal(map[string]string{"refresh_token": login.Refresh})
	status, data, _ = request("POST", "/api/v1/auth/refresh", "application/json", "", refreshJSON)
	var refresh struct {
		Token string `json:"token"`
	}
	if status != 200 || json.Unmarshal(data, &refresh) != nil || refresh.Token == "" {
		t.Fatal("refresh token did not survive application/database restart", status)
	}
	login.Token = refresh.Token
	status, data, _ = request("GET", "/api/v1/users/me", "", login.Token, nil)
	if status != 200 || !bytes.Contains(data, []byte(user.ID)) {
		t.Fatal("user identity did not persist", status)
	}
	verifyFile()
	if redisCommand("GET acceptance:persistent") != "verified" {
		t.Fatal("Redis value did not survive restart")
	}
	for name, uid := range claimUIDs {
		claim, e := c.kube.CoreV1().PersistentVolumeClaims(ns.Name).Get(ctx, name, metav1.GetOptions{})
		if e != nil || claim.UID != uid {
			t.Fatal("persistent claim replaced", name, e)
		}
	}
	pods, err := c.kube.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{Limit: 30})
	if err != nil || pods.Continue != "" {
		t.Fatal(err)
	}
	for _, pod := range pods.Items {
		if pod.Spec.NodeName != node.Name {
			t.Fatal("workload escaped owned worker")
		}
		if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
			t.Fatal("workload can receive cluster credentials")
		}
		for _, container := range pod.Spec.Containers {
			nonRoot := pod.Spec.SecurityContext != nil && pod.Spec.SecurityContext.RunAsNonRoot != nil && *pod.Spec.SecurityContext.RunAsNonRoot
			if container.SecurityContext != nil && container.SecurityContext.RunAsNonRoot != nil {
				nonRoot = *container.SecurityContext.RunAsNonRoot
			}
			if !nonRoot {
				t.Fatal("workload does not enforce non-root")
			}
		}
	}
	if databaseMode == "external" && redisMode == "external" && storageMode == "bundled" {
		xemTLSAcceptance(t, ctx, c, ns.Name)
	}
	t.Log("verified real admin and frontend login, host/auth rejection, private S3 upload and signed download, token refresh, unchanged PVCs and PostgreSQL/Redis/object persistence; no email sent")
}

func xemCandidate(t *testing.T, name, architecture, databaseMode, redisMode, storageMode string) spec.Application {
	t.Helper()
	raw, err := os.ReadFile("../../templates/blueprints/xem/hakopod.toml")
	if os.IsNotExist(err) {
		raw, err = os.ReadFile("../../templates/blueprints/xem/candidate.toml")
		t.Log("explicit prepublication candidate; image references are supplied by the local acceptance registry")
	}
	if err != nil {
		t.Fatal(err)
	}
	replacements := []string{"{{site_hostname}}", xemFixtureHostname, "https://catalog.example.test", "https://" + xemFixtureHost, "{{config.storage-bucket}}", "acceptance-private", "{{config.storage-endpoint}}", "http://storage:9000", "{{config.storage-region}}", "us-east-1", "{{config.admin-email}}", "admin@example.test", "{{config.admin-name}}", "Acceptance admin", "{{config.team-name}}", "Acceptance team"}
	base, err := spec.Parse([]byte(strings.NewReplacer(replacements...).Replace(string(raw))))
	if err != nil {
		t.Fatal(err)
	}
	base.Name = name
	app, err := spec.Normalize(base)
	if err != nil {
		t.Fatal(err)
	}
	deployable := false
	for _, entry := range spec.Templates() {
		if entry.ID == "xem" {
			deployable = entry.Deployable
		}
	}
	if deployable {
		// Production topology, mode validation and substitutions must use the same
		// planner as the dashboard/API. Fixtures are added only after it succeeds.
		app, err = spec.PlanTemplate("xem", spec.TemplateOptions{Name: name, Public: true, Architecture: architecture, StorageGiB: 1, SiteURL: "https://" + xemFixtureHost, Values: map[string]string{
			"database-mode": databaseMode, "database-host": "fixture-db", "database-port": "5432", "database-name": "externalxem", "database-user": "hakopod", "database-sslmode": "disable",
			"redis-mode": redisMode, "redis-host": "fixture-redis", "redis-port": "6379", "redis-username": "", "redis-db": "2", "redis-tls": "false",
			"storage-mode": storageMode, "admin-email": "admin@example.test", "admin-name": "Acceptance admin", "team-name": "Acceptance team", "storage-bucket": "acceptance-private", "storage-endpoint": "https://fixture.example.test", "storage-region": "us-east-1",
		}})
		if err != nil {
			t.Fatal("canonical Xem plan", err)
		}
	}
	for service, env := range map[string]string{"backend": "HAKOPOD_XEM_BACKEND_IMAGE", "frontend": "HAKOPOD_XEM_FRONTEND_IMAGE"} {
		image := os.Getenv(env)
		if image == "" {
			continue
		}
		parts := strings.Split(image, "@sha256:")
		if len(parts) != 2 || len(parts[1]) != 64 {
			t.Fatal(env + " must contain an observed registry manifest digest")
		}
		serviceSpec := app.Services[service]
		serviceSpec.Image = image
		app.Services[service] = serviceSpec
	}
	backend := app.Services["backend"]

	if databaseMode == "external" {
		if deployable {
			if _, exists := app.Services["db"]; exists {
				t.Fatal("canonical external PostgreSQL plan retained bundled db")
			}
		} else {
			delete(app.Services, "db")
			backend.Env["POSTGRES_HOST"] = "fixture-db"
			backend.Env["POSTGRES_DB"] = "externalxem"
		}
		db := base.Services["db"]
		db.Env["POSTGRES_DB"] = "externalxem"
		app.Services["fixture-db"] = db
		backend.DependsOn = xemFixtureDependency(backend.DependsOn, "db", "fixture-db")
	}
	if redisMode == "external" {
		if deployable {
			if _, exists := app.Services["redis"]; exists {
				t.Fatal("canonical external Redis plan retained bundled Redis")
			}
		} else {
			delete(app.Services, "redis")
			backend.Env["REDIS_HOST"] = "fixture-redis"
			backend.Env["REDIS_DB"] = "2"
		}
		app.Services["fixture-redis"] = base.Services["redis"]
		backend.DependsOn = xemFixtureDependency(backend.DependsOn, "redis", "fixture-redis")
	}
	if storageMode == "external" {
		if deployable {
			if _, exists := app.Services["storage"]; exists {
				t.Fatal("canonical external S3 plan retained bundled MinIO")
			}
		} else {
			delete(app.Services, "storage")
			proxy := app.Services["main"]
			delete(proxy.Files, "storage")
			app.Services["main"] = proxy
			delete(backend.Env, "S3_ACCESS_KEY")
			backend.Env["S3_BUCKET_NAME"] = "acceptance-private"
			backend.Env["S3_REGION"] = "us-east-1"
			delete(backend.Env, "S3_CREATE_BUCKET")
			delete(backend.Env, "S3_PUBLIC_ENDPOINT_URL")
			backend.Secrets["S3_ACCESS_KEY"] = spec.SecretRef{Ref: "storage-access-key"}
			backend.Secrets["S3_SECRET_KEY"] = spec.SecretRef{Ref: "storage-secret-key"}
		}
		// External storage is a real separately provisioned private S3 fixture.
		backend.Env["S3_ENDPOINT_URL"] = "http://fixture-storage:9000"
		app.Services["fixture-storage"] = spec.Service{Image: "pgsty/minio:RELEASE.2026-06-18T00-00-00Z@sha256:dacff8306a6e0a734518533992dbdcca26bc1ca47f77cf47cb9945725f92b29b", RunAsUser: 10001, RunAsGroup: 0, FSGroup: 0, Size: "large", Port: 9000, Args: []string{"server", "/data", "--console-address", ":9001"}, Secrets: map[string]spec.SecretRef{"MINIO_ROOT_USER": {Ref: "storage-access-key"}, "MINIO_ROOT_PASSWORD": {Ref: "storage-secret-key"}}, Healthcheck: "/minio/health/live", Networks: []string{"default"}, Volume: &spec.Volume{MountPath: "/data", SizeGiB: 1}, UpdateStrategy: "recreate"}
		backend.DependsOn = xemFixtureDependency(backend.DependsOn, "storage", "fixture-storage")
	} else {
		if _, exists := app.Services["storage"]; !exists {
			t.Fatal("bundled Xem plan must include MinIO")
		}
	}
	app.Services["backend"] = backend
	for name, s := range app.Services {
		s.Architecture = architecture
		if s.Volume != nil {
			s.Volume.SizeGiB = 1
		}
		app.Services[name] = s
	}
	app, err = spec.Normalize(app)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func xemFixtureDependency(deps []string, from, fixture string) []string {
	out := make([]string, 0, len(deps)+1)
	for _, name := range deps {
		if name != from {
			out = append(out, name)
		}
	}
	return append(out, fixture)
}

func xemSecrets(t *testing.T, app spec.Application) map[string]string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, ref := range spec.TemplateSecretNames(app) {
		random := make([]byte, 32)
		if _, err := rand.Read(random); err != nil {
			t.Fatal(err)
		}
		values[ref] = base64.RawURLEncoding.EncodeToString(random)
		if ref == "storage-user" {
			values[ref] = hex.EncodeToString(random[:16])
		}
	}
	if _, required := values["storage-access-key"]; required {
		values["storage-access-key"] = "fixture-access-key"
	}
	values["encryption-private-key"] = base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	return values
}

func xemRequest(t *testing.T, ctx context.Context, client *http.Client, address, method, path, contentType, token string, body []byte, cookies []*http.Cookie) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, "http://"+address+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = xemFixtureHost
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal("fixture HTTP request failed", method, path, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data, response.Header
}

func xemFrontendSession(t *testing.T, ctx context.Context, client *http.Client, address, password string) {
	t.Helper()
	status, data, _ := xemRequest(t, ctx, client, address, "GET", "/api/auth/providers", "", "", nil, nil)
	if status != 200 || !bytes.Contains(data, []byte(`"credentials"`)) {
		t.Fatal("frontend authentication provider unavailable", status)
	}
	status, data, headers := xemRequest(t, ctx, client, address, "GET", "/api/auth/csrf", "", "", nil, nil)
	var csrf struct {
		Token string `json:"csrfToken"`
	}
	if status != 200 || json.Unmarshal(data, &csrf) != nil || csrf.Token == "" {
		t.Fatal("frontend CSRF initialization failed", status)
	}
	cookies := (&http.Response{Header: headers}).Cookies()
	form := url.Values{"csrfToken": {csrf.Token}, "email": {"admin@example.test"}, "password": {password}, "callbackUrl": {"https://" + xemFixtureHost + "/"}, "json": {"true"}}
	status, _, headers = xemRequest(t, ctx, client, address, "POST", "/api/auth/callback/credentials", "application/x-www-form-urlencoded", "", []byte(form.Encode()), cookies)
	if status != 200 && status != 302 {
		t.Fatal("frontend credential login failed", status)
	}
	session := false
	for _, cookie := range (&http.Response{Header: headers}).Cookies() {
		cookies = append(cookies, cookie)
		if strings.Contains(cookie.Name, "session-token") {
			session = true
			if !cookie.Secure || !cookie.HttpOnly {
				t.Fatal("frontend session cookie lacks Secure/HttpOnly")
			}
		}
	}
	if !session {
		t.Fatal("frontend login did not issue a session cookie")
	}
	status, data, _ = xemRequest(t, ctx, client, address, "GET", "/api/auth/session", "", "", nil, cookies)
	if status != 200 || !bytes.Contains(data, []byte("admin@example.test")) {
		t.Fatal("frontend session lookup failed", status)
	}
}

func xemVerifyObject(t *testing.T, ctx context.Context, client *http.Client, store *s3.Client, storageAddress, publicAddress, storageService, storageMode, bucket, key, signed string, want []byte) {
	t.Helper()
	object, err := store.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &key})
	if err != nil {
		t.Fatal("authenticated object read", err)
	}
	data, err := io.ReadAll(io.LimitReader(object.Body, 1<<20))
	object.Body.Close()
	if err != nil || !bytes.Equal(data, want) {
		t.Fatal("stored object bytes differ")
	}
	address, host := storageAddress, storageService+":9000"
	if storageMode == "bundled" {
		address, host = publicAddress, xemFixtureHost
	}
	objectPath := "/" + bucket + "/" + url.PathEscape(key)
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+address+objectPath, nil)
	req.Host = host
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("anonymous object read was not denied", response.StatusCode)
	}
	u, err := url.Parse(signed)
	if err != nil || u.Host != host || u.Path != objectPath || u.Query().Get("X-Amz-Signature") == "" {
		t.Fatal("application returned an invalid signed object URL")
	}
	if storageMode == "bundled" && u.Scheme != "https" {
		t.Fatal("bundled signed URL must use the public HTTPS origin")
	}
	u.Host = address
	u.Scheme = "http" // The local forward carries the exact production Host/path/query.
	req, _ = http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	req.Host = host
	if storageMode == "bundled" {
		req.Header.Set("Authorization", "Bearer acceptance-marker")
		req.Header.Set("Cookie", "acceptance_session=marker")
	}
	response, err = client.Do(req)
	if err != nil {
		t.Fatal("application signed URL request failed")
	}
	data, err = io.ReadAll(io.LimitReader(response.Body, 1<<20))
	response.Body.Close()
	if response.StatusCode != 200 || err != nil || !bytes.Equal(data, want) {
		t.Fatal("application signed object URL did not read uploaded bytes", response.StatusCode)
	}
	if storageMode == "bundled" {
		if response.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatal("signed object response must not be cached")
		}
		req, _ = http.NewRequestWithContext(ctx, "PUT", u.String(), nil)
		req.Host = host
		response, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 405 {
			t.Fatal("public object proxy accepted a write method", response.StatusCode)
		}
	}
}

func xemPortForward(t *testing.T, ctx context.Context, path, namespace, service string, port int) (string, func()) {
	t.Helper()
	forward, stop := context.WithCancel(ctx)
	cmd := exec.CommandContext(forward, "kubectl", "--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", namespace, "port-forward", "service/"+service, fmt.Sprintf(":%d", port), "--address=127.0.0.1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stop()
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		stop()
		t.Fatal(err)
	}
	result := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			result <- scanner.Text()
		} else {
			result <- ""
		}
		_, _ = io.Copy(io.Discard, stdout)
	}()
	var line string
	select {
	case line = <-result:
	case <-time.After(20 * time.Second):
		stop()
		_ = cmd.Wait()
		t.Fatal("port forwarding start timed out")
	}
	fields := strings.Fields(line)
	if len(fields) < 3 || !strings.HasPrefix(line, "Forwarding from ") {
		stop()
		_ = cmd.Wait()
		t.Fatal("port forwarding did not start")
	}
	done := false
	return fields[2], func() {
		if !done {
			done = true
			stop()
			_ = cmd.Wait()
		}
	}
}

func xemCleanup(t *testing.T, c *Client, ns *corev1.Namespace, target Target, secrets map[string]string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	current, err := c.kube.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{})
	if err != nil {
		t.Error("cannot verify fixture namespace ownership; retaining resources", err)
		return
	}
	if current.UID != ns.UID || owned(current, target) != nil || current.Labels[xemFixtureLabel] != target.ApplicationID {
		t.Error("fixture namespace ownership changed; retaining resources")
		return
	}
	if t.Failed() {
		pods, e := c.kube.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{Limit: 30})
		if e == nil {
			for _, pod := range pods.Items {
				t.Logf("fixture pod %s phase=%s", pod.Name, pod.Status.Phase)
				for _, s := range pod.Status.ContainerStatuses {
					t.Logf("container %s ready=%t restarts=%d state=%v", s.Name, s.Ready, s.RestartCount, s.State)
				}
				data, e := c.kube.CoreV1().Pods(ns.Name).GetLogs(pod.Name, &corev1.PodLogOptions{TailLines: ptr(int64(30)), LimitBytes: ptr(int64(4000))}).DoRaw(ctx)
				if e == nil {
					log := string(data)
					for _, secret := range secrets {
						log = strings.ReplaceAll(log, secret, "[fixture secret]")
					}
					safe := []string{}
					for _, line := range strings.Split(log, "\n") {
						lower := strings.ToLower(line)
						if strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "authorization") || strings.Contains(lower, "private key") || strings.Contains(lower, "signature") || strings.Contains(lower, "insert into") || strings.Contains(lower, "update ") {
							continue
						}
						safe = append(safe, regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`).ReplaceAllString(line, "[redacted token]"))
					}
					t.Log("fixture diagnostics", strings.Join(safe, "\n"))
				}
			}
		}
	}
	claims, err := c.kube.CoreV1().PersistentVolumeClaims(ns.Name).List(ctx, metav1.ListOptions{Limit: 20})
	if err != nil || claims.Continue != "" {
		t.Error("cannot bound fixture claims", err)
		return
	}
	for ref := range secrets {
		if err := c.DeleteWorkloadSecret(ctx, target.Project, target.Environment, target.Spec.Name, ref); err != nil {
			t.Error(err)
		}
	}
	if err := c.kube.CoreV1().Namespaces().Delete(ctx, ns.Name, deleteOptions(current)); err != nil {
		t.Error(err)
		return
	}
	for ctx.Err() == nil {
		_, err := c.kube.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{})
		gone := apierrors.IsNotFound(err)
		for _, claim := range claims.Items {
			if claim.Spec.VolumeName != "" {
				_, err := c.kube.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
				gone = gone && apierrors.IsNotFound(err)
			}
		}
		if gone {
			t.Log("owned application namespace, claims and dynamic volumes reclaimed")
			return
		}
		time.Sleep(time.Second)
	}
	t.Error("fixture resource cleanup timed out")
}

// Optional coordination with an independent browser reviewer. Secrets are handed
// over only through an exclusive mode-0600 file, and removed before teardown.
func xemBrowserHandoff(t *testing.T, ctx context.Context, kubeconfig, namespace, address, password string) {
	t.Helper()
	path := os.Getenv("HAKOPOD_XEM_BROWSER_EVIDENCE_FILE")
	if path == "" {
		return
	}
	data, err := json.Marshal(map[string]string{"address": address, "host": xemFixtureHost, "namespace": namespace, "kubeconfig": kubeconfig, "email": "admin@example.test", "password": password, "done": path + ".done"})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("exclusive browser handoff file", err)
	}
	defer os.Remove(path)
	if _, err = file.Write(data); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	t.Log("independent browser fixture ready; credentials are in the configured mode-0600 handoff file")
	wait, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for wait.Err() == nil {
		if _, err := os.Stat(path + ".done"); err == nil {
			_ = os.Remove(path + ".done")
			return
		}
		if err := sleepContext(wait, time.Second); err != nil {
			break
		}
	}
	t.Fatal("independent browser review did not finish within the fixture window")
}

package cluster

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
)

const outpostFixtureLabel = "hakopod.io/outpost-acceptance"
const outpostReceiverImage = "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a"

// External dependencies are separately named, same-namespace development
// fixtures. This verifies the real external configuration paths, not arbitrary
// provider networks or TLS. Delivery stays inside an internal fixture network.
func TestLiveOutpostTemplate(t *testing.T) {
	if os.Getenv("HAKOPOD_OUTPOST_TEST") != "1" {
		t.Skip("set HAKOPOD_OUTPOST_TEST=1 for Outpost runtime acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires explicit k3d-hakopod-dev kubeconfig")
	}
	// One combination per disposable CI runner keeps time and resource use
	// bounded. The three bits select external PostgreSQL, Redis and RabbitMQ.
	modes := os.Getenv("HAKOPOD_OUTPOST_TEST_MODES")
	if len(modes) != 3 || strings.Trim(modes, "01") != "" {
		t.Fatal("HAKOPOD_OUTPOST_TEST_MODES must be three binary digits in PostgreSQL/Redis/RabbitMQ order")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 22*time.Minute)
	defer cancel()
	c, err := New(path, Options{AppDomain: "127.0.0.1.sslip.io", RolloutTimeout: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	node, err := c.kube.CoreV1().Nodes().Get(ctx, "k3d-hakopod-dev-server-0", metav1.GetOptions{})
	if err != nil {
		t.Fatal("named development node unavailable", err)
	}
	for _, condition := range node.Status.Conditions {
		if (condition.Type == corev1.NodeDiskPressure || condition.Type == corev1.NodeMemoryPressure || condition.Type == corev1.NodePIDPressure) && condition.Status != corev1.ConditionFalse {
			t.Fatal("development node is under resource pressure", condition.Type)
		}
	}
	runID := fmt.Sprintf("outpost-%d", time.Now().UnixNano())
	app, secrets := outpostFixture(t, runID, modes, node)
	mathesarCapacity(t, ctx, c, node, app)
	t.Logf("Outpost actual runtime architecture=%s dependency modes=%s (1=external development fixture)", node.Status.NodeInfo.Architecture, modes)
	for _, name := range spec.Names(app) {
		t.Logf("service %s image=%s", name, app.Services[name].Image)
	}
	target := Target{ApplicationID: runID, Project: "outpost-acceptance", Environment: "test", OperationID: "initial", Revision: 1, Spec: app}
	labels := labelsFor(target, "")
	labels[outpostFixtureLabel] = runID
	ns, err := c.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(runID), Labels: labels}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer outpostCleanup(t, c, ns, target, secrets)
	for ref, value := range secrets {
		if err := c.CreateWorkloadSecret(ctx, target.Project, target.Environment, app.Name, ref, value); err != nil {
			t.Fatal("create scoped fixture secret", ref, err)
		}
	}
	deploy := func() {
		t.Helper()
		if _, err := c.Deploy(ctx, target, func(e Event) { t.Log(e.Type, e.Service, e.Message) }); err != nil {
			t.Fatal("Outpost deployment", err)
		}
	}
	deploy()
	initialPods := outpostVerifyRuntime(t, ctx, c, target, node.Name)
	address, stop := xemPortForward(t, ctx, path, ns.Name, "main", 3333)
	defer stop()
	receiverAddress, receiverStop := xemPortForward(t, ctx, path, ns.Name, "fixture-receiver", 8080)
	defer receiverStop()
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	request := func(method, route, token string, body any) (int, []byte) {
		t.Helper()
		return outpostRequest(t, ctx, client, address, method, route, token, body)
	}
	outpostVerifyHealth(t, ctx, path, ns.Name, client)
	for _, token := range []string{"", "invalid-fixture-key"} {
		status, _ := request("GET", "/api/v1/tenants", token, nil)
		if status != http.StatusUnauthorized {
			t.Fatal("unauthorized tenant access accepted", status)
		}
	}
	const tenantID, otherTenantID, destinationID = "fixture-tenant", "fixture-other", "fixture-destination"
	apiKey := secrets["api-key"]
	for _, id := range []string{tenantID, otherTenantID} {
		status, body := request("PUT", "/api/v1/tenants/"+id, apiKey, map[string]any{"metadata": map[string]string{"fixture": runID}})
		var tenant struct {
			ID string `json:"id"`
		}
		if status != http.StatusCreated || json.Unmarshal(body, &tenant) != nil || tenant.ID != id {
			t.Fatal("create real tenant failed", status)
		}
	}
	getToken := func(id string) string {
		t.Helper()
		status, body := request("GET", "/api/v1/tenants/"+id+"/token", apiKey, nil)
		var token struct {
			Token    string `json:"token"`
			TenantID string `json:"tenant_id"`
		}
		if status != http.StatusOK || json.Unmarshal(body, &token) != nil || token.Token == "" || token.TenantID != id {
			t.Fatal("tenant token creation failed", status)
		}
		return token.Token
	}
	tenantToken, otherToken := getToken(tenantID), getToken(otherTenantID)
	status, _ := request("GET", "/api/v1/tenants/"+otherTenantID, tenantToken, nil)
	if status != http.StatusForbidden {
		t.Fatal("tenant JWT crossed tenant boundary", status)
	}
	status, _ = request("POST", "/api/v1/publish", tenantToken, map[string]any{"tenant_id": tenantID, "data": map[string]string{"marker": "forbidden"}})
	if status != http.StatusForbidden {
		t.Fatal("tenant JWT reached administrator-only publish", status)
	}
	destinationRoute := "/api/v1/tenants/" + tenantID + "/destinations/" + destinationID
	status, body := request("POST", "/api/v1/tenants/"+tenantID+"/destinations", tenantToken, map[string]any{"id": destinationID, "type": "webhook", "topics": []string{"*"}, "config": map[string]string{"url": "http://fixture-receiver:8080/deliver"}})
	var destination struct {
		ID          string            `json:"id"`
		Type        string            `json:"type"`
		Credentials map[string]string `json:"credentials"`
	}
	if status != http.StatusCreated || json.Unmarshal(body, &destination) != nil || destination.ID != destinationID || destination.Type != "webhook" || destination.Credentials["secret"] == "" {
		t.Fatal("create real private webhook destination failed", status)
	}
	signingSecret := destination.Credentials["secret"]
	publish := func(eventID, marker string) {
		t.Helper()
		status, _ := request("POST", "/api/v1/publish", apiKey, map[string]any{"id": eventID, "tenant_id": tenantID, "destination_id": destinationID, "data": map[string]string{"marker": marker}})
		if status != http.StatusAccepted {
			t.Fatal("publish real event failed", status)
		}
	}
	verifyDelivery := func(eventID, marker, previousAttemptID string) string {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		for ctx.Err() == nil && time.Now().Before(deadline) {
			status, data := request("GET", "/api/v1/events/"+eventID+"?tenant_id="+tenantID, apiKey, nil)
			var event struct {
				ID           string            `json:"id"`
				TenantID     string            `json:"tenant_id"`
				Data         map[string]string `json:"data"`
				Destinations []string          `json:"matched_destination_ids"`
			}
			eventOK := status == http.StatusOK && json.Unmarshal(data, &event) == nil && event.ID == eventID && event.TenantID == tenantID && event.Data["marker"] == marker && slices.Contains(event.Destinations, destinationID)
			status, data = request("GET", destinationRoute+"/attempts?limit=20", tenantToken, nil)
			var attempts struct {
				Models []struct {
					ID            string `json:"id"`
					TenantID      string `json:"tenant_id"`
					EventID       string `json:"event_id"`
					DestinationID string `json:"destination_id"`
					Status        string `json:"status"`
					Code          string `json:"code"`
				} `json:"models"`
			}
			attemptID := ""
			if status == http.StatusOK && json.Unmarshal(data, &attempts) == nil && len(attempts.Models) <= 20 {
				for _, attempt := range attempts.Models {
					if attempt.EventID == eventID && attempt.TenantID == tenantID && attempt.DestinationID == destinationID && attempt.Status == "success" && attempt.Code == "200" && attempt.ID != "" && (previousAttemptID == "" || attempt.ID == previousAttemptID) {
						attemptID = attempt.ID
					}
				}
			}
			status, data = outpostRequest(t, ctx, client, receiverAddress, "GET", "/events", "", nil)
			var received []map[string]string
			receivedOK := false
			if status == http.StatusOK && json.Unmarshal(data, &received) == nil && len(received) <= 32 {
				for _, payload := range received {
					receivedOK = receivedOK || payload["marker"] == marker
				}
			}
			if eventOK && attemptID != "" && receivedOK {
				t.Logf("verified private HTTP delivery, persisted event and successful attempt for %s", eventID)
				return attemptID
			}
			if sleepContext(ctx, time.Second) != nil {
				break
			}
		}
		t.Fatal("real delivery, persisted event and successful attempt did not converge", eventID)
		return ""
	}
	const eventID = "fixture-event-before-restart"
	publish(eventID, runID+"-before")
	attemptID := verifyDelivery(eventID, runID+"-before", "")
	status, _ = request("GET", "/api/v1/events/"+eventID, otherToken, nil)
	if status != http.StatusNotFound {
		t.Fatal("another tenant could read the persisted event", status)
	}
	status, _ = request("GET", destinationRoute+"/attempts", otherToken, nil)
	if status != http.StatusForbidden {
		t.Fatal("another tenant could list destination attempts", status)
	}
	redisService, redisDB := "redis", "0"
	if modes[1] == '1' {
		redisService, redisDB = "fixture-redis", "2"
	}
	redisCommand := func(command string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", ns.Name, "exec", "deployment/"+redisService, "--", "sh", "-ec", `REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli -n `+redisDB+" "+command)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal("fixture Redis command failed", err)
		}
		return strings.TrimSpace(string(out))
	}
	verifyRedisSelection := func() {
		t.Helper()
		pods, err := c.kube.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{LabelSelector: serviceKey + "=main", Limit: 10})
		if err != nil || pods.Continue != "" {
			t.Fatal("cannot observe API Redis connection identity", err)
		}
		ips := map[string]bool{}
		for _, pod := range pods.Items {
			if pod.DeletionTimestamp == nil && net.ParseIP(pod.Status.PodIP) != nil {
				ips[pod.Status.PodIP] = true
			}
		}
		initialized, _, err := xemRedisConnections(redisCommand("CLIENT LIST"), ips, redisDB)
		if err != nil {
			t.Fatal("Outpost API Redis database selection", err)
		}
		t.Logf("observed %d initialized API connections to Redis database %s", initialized, redisDB)
	}
	verifyRedisSelection()
	if redisCommand("SET acceptance:persistent verified") != "OK" || redisCommand("SAVE") != "OK" {
		t.Fatal("Redis durable checkpoint failed")
	}
	claims, err := c.kube.CoreV1().PersistentVolumeClaims(ns.Name).List(ctx, metav1.ListOptions{Limit: 10})
	if err != nil || claims.Continue != "" || len(claims.Items) != 3 {
		t.Fatal("expected exactly three owned dependency claims", err)
	}
	claimUIDs := map[string]types.UID{}
	for _, claim := range claims.Items {
		if owned(&claim, target) != nil || claim.Status.Phase != corev1.ClaimBound {
			t.Fatal("dependency claim is not owned and bound")
		}
		claimUIDs[claim.Name] = claim.UID
	}
	stop()
	for name, service := range target.Spec.Services {
		if name != "fixture-receiver" {
			service.RestartNonce = "durability"
			target.Spec.Services[name] = service
		}
	}
	target.Revision++
	target.OperationID = "restart"
	deploy()
	afterPods := outpostVerifyRuntime(t, ctx, c, target, node.Name)
	for name, uid := range initialPods {
		if name != "fixture-receiver" && afterPods[name] == uid {
			t.Fatal("service did not actually restart", name)
		}
	}
	address, stop = xemPortForward(t, ctx, path, ns.Name, "main", 3333)
	defer stop()
	client.CloseIdleConnections()
	outpostVerifyHealth(t, ctx, path, ns.Name, client)
	status, body = request("GET", "/api/v1/tenants/"+tenantID, tenantToken, nil)
	var tenant struct {
		ID       string            `json:"id"`
		Metadata map[string]string `json:"metadata"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &tenant) != nil || tenant.ID != tenantID || tenant.Metadata["fixture"] != runID {
		t.Fatal("tenant and existing JWT did not survive restart", status)
	}
	status, body = request("GET", destinationRoute, tenantToken, nil)
	destination.Credentials = nil
	if status != http.StatusOK || json.Unmarshal(body, &destination) != nil || destination.ID != destinationID || destination.Credentials["secret"] != signingSecret {
		t.Fatal("destination and encrypted signing credentials did not persist", status)
	}
	verifyDelivery(eventID, runID+"-before", attemptID)
	verifyRedisSelection()
	if redisCommand("GET acceptance:persistent") != "verified" {
		t.Fatal("Redis durable value did not survive restart")
	}
	for name, uid := range claimUIDs {
		claim, err := c.kube.CoreV1().PersistentVolumeClaims(ns.Name).Get(ctx, name, metav1.GetOptions{})
		if err != nil || claim.UID != uid || claim.Status.Phase != corev1.ClaimBound {
			t.Fatal("dependency claim changed across restart", name, err)
		}
	}
	publish("fixture-event-after-restart", runID+"-after")
	verifyDelivery("fixture-event-after-restart", runID+"-after", "")
	outpostVerifyHealth(t, ctx, path, ns.Name, client)
	t.Log("verified migration ordering, API/JWT isolation, encrypted destination persistence, real private delivery, persisted event and identical attempt, all dependency restarts, unchanged PVCs and Redis database selection; no public delivery or external-provider qualification")
}

func outpostFixture(t *testing.T, name, modes string, node *corev1.Node) (spec.Application, map[string]string) {
	t.Helper()
	values := map[string]string{"redis-host": "fixture-redis", "redis-port": "6379", "redis-username": "default", "redis-database": "2", "redis-tls": "false"}
	for i, field := range []string{"database-mode", "redis-mode", "broker-mode"} {
		values[field] = "bundled"
		if modes[i] == '1' {
			values[field] = "external"
		}
	}
	app, err := spec.PlanTemplate("outpost", spec.TemplateOptions{Name: name, Public: true, StorageGiB: 1, Values: values})
	if err != nil {
		t.Fatal(err)
	}
	base, err := spec.PlanTemplate("outpost", spec.TemplateOptions{Name: name, Public: true, StorageGiB: 1})
	if err != nil {
		t.Fatal(err)
	}
	for i, dependency := range []string{"db", "redis", "broker"} {
		if modes[i] != '1' {
			continue
		}
		fixture := "fixture-" + dependency
		app.Services[fixture] = base.Services[dependency]
		for _, role := range []string{"main", "delivery", "log", "migrate"} {
			if role == "migrate" && dependency == "broker" {
				continue
			}
			service := app.Services[role]
			service.DependsOn = append(service.DependsOn, fixture)
			app.Services[role] = service
		}
	}
	app.Services["fixture-receiver"] = spec.Service{Image: outpostReceiverImage, Port: 8080, Public: false, Size: "small", Replicas: 1, RunAsUser: 65532, RunAsGroup: 65532, ReadOnlyRootFilesystem: true, Networks: []string{"default"}, Healthcheck: "/healthz", Command: []string{"python3", "-B", "-u", "-c"}, Args: []string{outpostReceiverScript}, Resources: &spec.Resources{CPURequest: "10m", CPULimit: "100m", MemoryRequest: "24Mi", MemoryLimit: "64Mi"}}
	// The receiver is deliberately private and this fixture has no Internet
	// egress. The catalog's production network choice is otherwise unchanged.
	app.Networks["default"] = spec.Network{Internal: true}
	for name, service := range app.Services {
		service.NodeName, service.Architecture = node.Name, node.Status.NodeInfo.Architecture
		app.Services[name] = service
	}
	app, err = spec.Normalize(app)
	if err != nil {
		t.Fatal(err)
	}
	secrets := map[string]string{}
	for _, ref := range spec.TemplateSecretNames(app) {
		raw := make([]byte, 24)
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		secrets[ref] = hex.EncodeToString(raw)
	}
	secrets["encryption-secret"] = secrets["encryption-secret"][:32]
	if modes[0] == '1' {
		u := url.URL{Scheme: "postgres", User: url.UserPassword("hakopod", secrets["database-password"]), Host: "fixture-db:5432", Path: "/outpost", RawQuery: "sslmode=disable"}
		secrets["database-url"] = u.String()
	}
	if modes[2] == '1' {
		u := url.URL{Scheme: "amqp", User: url.UserPassword("hakopod", secrets["broker-password"]), Host: "fixture-broker:5672", Path: "/outpost"}
		secrets["broker-url"] = u.String()
	}
	return app, secrets
}

func outpostRequest(t *testing.T, ctx context.Context, client *http.Client, address, method, path, token string, payload any) (int, []byte) {
	t.Helper()
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+address+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal("create fixture request", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal("fixture HTTP request failed", method, path, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
	if err != nil || len(data) > 256<<10 {
		t.Fatal("fixture response exceeded read bound", err)
	}
	return response.StatusCode, data
}

func outpostVerifyHealth(t *testing.T, ctx context.Context, path, namespace string, client *http.Client) {
	t.Helper()
	for _, role := range []string{"main", "delivery", "log"} {
		func() {
			address, stop := xemPortForward(t, ctx, path, namespace, role, 3333)
			defer stop()
			deadline := time.Now().Add(60 * time.Second)
			for ctx.Err() == nil && time.Now().Before(deadline) {
				status, body := outpostRequest(t, ctx, client, address, "GET", "/healthz", "", nil)
				var health struct {
					Status  string `json:"status"`
					Workers map[string]struct {
						Status string `json:"status"`
					} `json:"workers"`
				}
				ok := status == http.StatusOK && json.Unmarshal(body, &health) == nil && health.Status == "healthy" && len(health.Workers) >= 2
				for _, worker := range health.Workers {
					ok = ok && worker.Status == "healthy"
				}
				if ok {
					t.Log("all workers healthy", role)
					return
				}
				if sleepContext(ctx, time.Second) != nil {
					break
				}
			}
			t.Fatal("worker readiness remained incomplete or degraded", role)
		}()
	}
}

func outpostVerifyRuntime(t *testing.T, ctx context.Context, c *Client, target Target, nodeName string) map[string]types.UID {
	t.Helper()
	namespace := Namespace(target.ApplicationID)
	job, err := c.kube.BatchV1().Jobs(namespace).Get(ctx, jobName("migrate"), metav1.GetOptions{})
	if err != nil || owned(job, target) != nil || job.Annotations[jobRevision] != strconv.FormatInt(target.Revision, 10) || job.Status.Succeeded != 1 || job.Status.CompletionTime == nil {
		t.Fatal("migration job did not complete for this revision", err)
	}
	pods, err := c.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{Limit: 30})
	if err != nil || pods.Continue != "" {
		t.Fatal("cannot bound application pod inspection", err)
	}
	observed := map[string]types.UID{}
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil {
			continue
		}
		name := pod.Labels[serviceKey]
		service, expected := target.Spec.Services[name]
		if !expected || owned(&pod, target) != nil || pod.Spec.NodeName != nodeName {
			t.Fatal("unexpected fixture pod ownership or placement")
		}
		if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
			t.Fatal("workload can receive cluster credentials")
		}
		if len(pod.Spec.Containers) != 1 {
			t.Fatal("unexpected fixture sidecar")
		}
		container := pod.Spec.Containers[0]
		if container.Image != service.Image || !strings.Contains(container.Image, "@sha256:") {
			t.Fatal("runtime image does not match immutable catalog plan", name)
		}
		nonRoot := pod.Spec.SecurityContext != nil && pod.Spec.SecurityContext.RunAsNonRoot != nil && *pod.Spec.SecurityContext.RunAsNonRoot
		if container.SecurityContext != nil && container.SecurityContext.RunAsNonRoot != nil {
			nonRoot = *container.SecurityContext.RunAsNonRoot
		}
		if !nonRoot || pod.Spec.SecurityContext == nil || pod.Spec.SecurityContext.RunAsUser == nil || *pod.Spec.SecurityContext.RunAsUser != service.RunAsUser {
			t.Fatal("workload does not enforce non-root", name)
		}
		if service.ReadOnlyRootFilesystem && (container.SecurityContext == nil || container.SecurityContext.ReadOnlyRootFilesystem == nil || !*container.SecurityContext.ReadOnlyRootFilesystem) {
			t.Fatal("workload lost read-only root filesystem", name)
		}
		if service.Job != nil {
			if pod.Status.Phase != corev1.PodSucceeded {
				t.Fatal("migration pod did not succeed")
			}
		} else {
			if pod.Status.Phase != corev1.PodRunning || len(pod.Status.ContainerStatuses) != 1 || !pod.Status.ContainerStatuses[0].Ready {
				t.Fatal("service is not running and ready", name)
			}
			if slices.Contains([]string{"main", "delivery", "log"}, name) {
				state := pod.Status.ContainerStatuses[0].State.Running
				if state == nil || state.StartedAt.Before(job.Status.CompletionTime) {
					t.Fatal("server started before migration completed", name)
				}
			}
		}
		if observed[name] != "" {
			t.Fatal("multiple current pods for a fixture service", name)
		}
		observed[name] = pod.UID
	}
	if len(observed) != len(target.Spec.Services) {
		t.Fatal("missing runtime service evidence")
	}
	services, err := c.kube.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{Limit: 20})
	if err != nil || services.Continue != "" {
		t.Fatal("cannot inspect service exposure", err)
	}
	for _, service := range services.Items {
		if service.Spec.Type != corev1.ServiceTypeClusterIP || len(service.Spec.ExternalIPs) != 0 {
			t.Fatal("fixture service unexpectedly exposed outside cluster")
		}
	}
	ingresses, err := c.kube.NetworkingV1().Ingresses(namespace).List(ctx, metav1.ListOptions{Limit: 10})
	if err != nil || ingresses.Continue != "" || len(ingresses.Items) != 1 {
		t.Fatal("expected only one public API ingress", err)
	}
	for _, rule := range ingresses.Items[0].Spec.Rules {
		if rule.HTTP == nil {
			t.Fatal("missing API ingress HTTP rule")
		}
		for _, path := range rule.HTTP.Paths {
			if path.Backend.Service == nil || path.Backend.Service.Name != "main" {
				t.Fatal("non-API service has public ingress")
			}
		}
	}
	t.Logf("verified revision %d migration %s completed before server startup; all %d services enforce pinned images, non-root and no cluster credentials", target.Revision, job.UID, len(observed))
	return observed
}

func outpostCleanup(t *testing.T, c *Client, ns *corev1.Namespace, target Target, secrets map[string]string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	current, err := c.kube.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{})
	if err != nil || current.UID != ns.UID || owned(current, target) != nil || current.Labels[outpostFixtureLabel] != target.ApplicationID {
		t.Error("fixture namespace ownership changed; retaining resources", err)
		return
	}
	if t.Failed() {
		pods, err := c.kube.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{Limit: 30})
		if err == nil && pods.Continue == "" {
			for _, pod := range pods.Items {
				t.Logf("fixture pod %s phase=%s", pod.Name, pod.Status.Phase)
				for _, status := range pod.Status.ContainerStatuses {
					reason, exit := "", int32(0)
					if status.State.Waiting != nil {
						reason = status.State.Waiting.Reason
					}
					if status.State.Terminated != nil {
						reason, exit = status.State.Terminated.Reason, status.State.Terminated.ExitCode
					}
					t.Logf("container %s ready=%t restarts=%d reason=%s exit=%d", status.Name, status.Ready, status.RestartCount, reason, exit)
				}
			}
		}
	}
	claims, err := c.kube.CoreV1().PersistentVolumeClaims(ns.Name).List(ctx, metav1.ListOptions{Limit: 10})
	if err != nil || claims.Continue != "" {
		t.Error("cannot bound fixture claims; retaining resources", err)
		return
	}
	for _, claim := range claims.Items {
		if owned(&claim, target) != nil {
			t.Error("unowned claim; retaining resources")
			return
		}
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
			t.Log("owned fixture namespace, scoped secrets, claims and dynamic volumes reclaimed")
			return
		}
		if sleepContext(ctx, time.Second) != nil {
			break
		}
	}
	t.Error("fixture resource cleanup timed out")
}

const outpostReceiverScript = `import json
from http.server import BaseHTTPRequestHandler, HTTPServer
events = []
class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def reply(self, status, payload):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def do_GET(self):
        if self.path == "/healthz": self.reply(200, {"ready": True})
        elif self.path == "/events": self.reply(200, events)
        else: self.reply(404, {})
    def do_POST(self):
        if self.path != "/deliver": return self.reply(404, {})
        try: size = int(self.headers.get("Content-Length", "0"))
        except ValueError: return self.reply(400, {})
        if not 0 < size <= 65536 or len(events) >= 32: return self.reply(413, {})
        self.connection.settimeout(10)
        try: value = json.loads(self.rfile.read(size))
        except (ValueError, TimeoutError): return self.reply(400, {})
        if not isinstance(value, dict) or not isinstance(value.get("marker"), str): return self.reply(422, {})
        events.append({"marker": value["marker"]})
        self.reply(200, {"accepted": True})
HTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
`

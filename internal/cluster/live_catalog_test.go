package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// This deliberately runs one catalog application at a time. It uses the actual
// FromTemplate specification and removes the entire marked fixture namespace
// and its dynamically provisioned volume before starting the next application.
func TestLiveCatalogTemplates(t *testing.T) {
	if os.Getenv("HAKOPOD_CATALOG_TEST") != "1" {
		t.Skip("set HAKOPOD_CATALOG_TEST=1 for sequential disposable template acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("catalog test requires the named k3d-hakopod-dev context")
	}
	client, err := New(path, Options{AppDomain: "127.0.0.1.sslip.io", RolloutTimeout: 180 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	cleanupFailed := false
	for _, id := range []string{"valkey", "uptime-kuma", "gitea"} {
		if selected := os.Getenv("HAKOPOD_CATALOG_TEMPLATE"); selected != "" && selected != id {
			continue
		}
		t.Run(id, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			app, err := spec.FromTemplate(id, "catalog-"+id, false, 1, "", "")
			if err != nil {
				t.Fatal(err)
			}
			app, err = client.Resolve(ctx, app)
			if err != nil {
				t.Fatal(err)
			}
			target := Target{Project: "catalog-test", Environment: "test", ApplicationID: "catalog-live-" + id + "-" + fmt.Sprint(time.Now().UnixNano()), OperationID: "catalog-initial", Revision: 1, Spec: app}
			labels := labelsFor(target, "")
			labels["hakopod.io/acceptance"] = "catalog"
			namespace, err := client.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labels}}, metav1.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			volumeName := ""
			defer func() {
				clean, done := context.WithTimeout(context.Background(), 90*time.Second)
				defer done()
				if id == "valkey" {
					if err := client.DeleteWorkloadSecret(clean, target.Project, target.Environment, target.Spec.Name, "database-password"); err != nil {
						t.Error("fixture secret cleanup failed", err)
						cleanupFailed = true
					}
				}
				current, err := client.kube.CoreV1().Namespaces().Get(clean, namespace.Name, metav1.GetOptions{})
				if err != nil || current.UID != namespace.UID || owned(current, target) != nil || current.Labels["hakopod.io/acceptance"] != "catalog" {
					t.Error("fixture namespace ownership changed; retained for diagnosis")
					cleanupFailed = true
					return
				}
				if claim, err := client.kube.CoreV1().PersistentVolumeClaims(namespace.Name).Get(clean, "main-data", metav1.GetOptions{}); err == nil {
					volumeName = claim.Spec.VolumeName
				}
				if err = client.kube.CoreV1().Namespaces().Delete(clean, namespace.Name, deleteOptions(current)); err != nil {
					t.Error("fixture namespace cleanup failed", err)
					cleanupFailed = true
					return
				}
				for {
					_, nsErr := client.kube.CoreV1().Namespaces().Get(clean, namespace.Name, metav1.GetOptions{})
					volumeGone := volumeName == ""
					if !volumeGone {
						_, pvErr := client.kube.CoreV1().PersistentVolumes().Get(clean, volumeName, metav1.GetOptions{})
						volumeGone = apierrors.IsNotFound(pvErr)
					}
					if apierrors.IsNotFound(nsErr) && volumeGone {
						t.Log("fixture namespace, PVC and dynamic PV removed")
						return
					}
					if clean.Err() != nil {
						t.Error("fixture cleanup did not finish; stopping further templates")
						cleanupFailed = true
						return
					}
					time.Sleep(500 * time.Millisecond)
				}
			}()
			if id == "valkey" {
				if err = client.PutWorkloadSecret(ctx, target.Project, target.Environment, target.Spec.Name, "database-password", "catalog-test-password-only"); err != nil {
					t.Fatal(err)
				}
			}
			if err = client.bootstrap(ctx, target); err != nil {
				t.Fatal(err)
			}
			if err = client.prepareStorage(ctx, target, "main", app.Services["main"]); err != nil {
				t.Fatal(err)
			}
			claim, err := client.kube.CoreV1().PersistentVolumeClaims(namespace.Name).Get(ctx, "main-data", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			claim.Labels["hakopod.io/acceptance"] = "catalog"
			if _, err = client.kube.CoreV1().PersistentVolumeClaims(namespace.Name).Update(ctx, claim, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err = client.Deploy(ctx, target, func(event Event) { t.Log(event.Type, event.Message) }); err != nil {
				out, _ := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", namespace.Name, "logs", "deployment/main", "--tail=35").CombinedOutput()
				message := strings.ReplaceAll(string(out), "catalog-test-password-only", "[redacted]")
				if len(message) > 6000 {
					message = message[:6000]
				}
				t.Fatalf("template startup failed: %v; bounded fixture log: %s", err, message)
			}
			run := func(args ...string) string {
				t.Helper()
				base := []string{"--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", namespace.Name, "exec", "deployment/main", "--"}
				out, err := exec.CommandContext(ctx, "kubectl", append(base, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("catalog behavior probe failed: %v; %s", err, strings.ReplaceAll(string(out), "catalog-test-password-only", "[redacted]"))
				}
				return strings.TrimSpace(string(out))
			}
			switch id {
			case "valkey":
				if got := run("valkey-cli", "--raw", "-h", "main", "PING"); !strings.Contains(got, "NOAUTH") {
					t.Fatalf("unauthenticated Valkey request unexpectedly accepted: %s", got)
				}
				if got := run("sh", "-c", `VALKEYCLI_AUTH="$VALKEY_PASSWORD" valkey-cli --raw -h main PING`); got != "PONG" {
					t.Fatalf("configured password did not authenticate to Valkey: %s", got)
				}
				if got := run("sh", "-c", `VALKEYCLI_AUTH="$VALKEY_PASSWORD" valkey-cli --raw -h main SET catalog-probe ok`); got != "OK" {
					t.Fatalf("Valkey SET failed: %s", got)
				}
				if got := run("sh", "-c", `VALKEYCLI_AUTH="$VALKEY_PASSWORD" valkey-cli --raw -h main GET catalog-probe`); got != "ok" {
					t.Fatalf("Valkey GET failed: %s", got)
				}
				t.Log("Valkey rejects unauthenticated access and passes authenticated PING/SET/GET through Service DNS")
			case "uptime-kuma":
				got := run("node", "-e", `fetch("http://main:3001/").then(async r=>{const t=await r.text();if(r.status!==200||!t.includes("Uptime Kuma"))process.exit(2);console.log("Uptime Kuma setup page HTTP 200")}).catch(()=>process.exit(3))`)
				t.Log(got)
			case "gitea":
				got := run("sh", "-c", `wget -qO /tmp/hakopod-health http://main:3000/api/healthz && if grep -q '"status":"pass"' /tmp/hakopod-health; then echo 'Gitea health status pass'; elif grep -qi 'gitea' /tmp/hakopod-health; then echo 'Gitea administrator setup page available'; else exit 2; fi`)
				t.Log(got)
				verifyCatalogGiteaSetup(t, ctx, client, path, &target)
			}
			claim, err = client.kube.CoreV1().PersistentVolumeClaims(namespace.Name).Get(ctx, "main-data", metav1.GetOptions{})
			if err != nil || claim.Status.Phase != corev1.ClaimBound {
				t.Fatal("catalog storage is not bound", err)
			}
			volumeName = claim.Spec.VolumeName
			runtime, err := client.ServiceRuntime(ctx, target, "main")
			if err == nil && runtime.Metrics.Available && runtime.Metrics.Memory != nil {
				t.Logf("observed memory: %d bytes", *runtime.Metrics.Memory)
			}
		})
		if cleanupFailed {
			t.Fatal("catalog cleanup requires attention; no next template was started")
		}
	}
}

func verifyCatalogGiteaSetup(t *testing.T, ctx context.Context, client *Client, path string, target *Target) {
	t.Helper()
	namespace := Namespace(target.ApplicationID)
	execute := func(input string, args ...string) (string, error) {
		base := []string{"--kubeconfig", path, "--context", "k3d-hakopod-dev", "-n", namespace, "exec", "-i", "deployment/main", "--"}
		command := exec.CommandContext(ctx, "kubectl", append(base, args...)...)
		command.Stdin = strings.NewReader(input)
		out, err := command.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	page, err := execute("", "curl", "--fail", "--silent", "--show-error", "--cookie-jar", "/tmp/catalog-cookies", "http://main:3000/")
	if err != nil {
		t.Fatal("Gitea setup page unavailable")
	}
	csrf := ""
	for _, pattern := range []string{`name="_csrf"[^>]*(?:value|content)="([^"]+)"`, `(?:value|content)="([^"]+)"[^>]*name="_csrf"`} {
		if match := regexp.MustCompile(pattern).FindStringSubmatch(page); len(match) > 1 {
			csrf = match[1]
			break
		}
	}
	password := "CatalogFixture-7!-" + fmt.Sprint(time.Now().UnixNano())
	form := url.Values{"_csrf": {csrf}, "db_type": {"sqlite3"}, "db_path": {"/var/lib/gitea/data/gitea.db"}, "app_name": {"Hakopod Catalog Fixture"}, "repo_root_path": {"/var/lib/gitea/git/repositories"}, "run_user": {"git"}, "domain": {"main"}, "http_port": {"3000"}, "ssh_port": {"0"}, "app_url": {"http://main:3000/"}, "log_root_path": {"/var/lib/gitea/data/log"}, "admin_name": {"catalogadmin"}, "admin_email": {"catalog-fixture@example.invalid"}, "admin_passwd": {password}, "admin_confirm_passwd": {password}, "disable_registration": {"true"}, "no_reply_address": {"noreply.example.invalid"}}
	status, err := execute(form.Encode(), "curl", "--silent", "--show-error", "--max-time", "60", "--cookie", "/tmp/catalog-cookies", "--output", "/tmp/catalog-install-result", "--write-out", "%{http_code}", "--data-binary", "@-", "http://main:3000/")
	if err != nil || status != "200" && status != "302" && status != "303" {
		t.Fatalf("Gitea administrator setup failed with HTTP %s", status)
	}
	verify := func() {
		t.Helper()
		deadline := time.Now().Add(40 * time.Second)
		for {
			result, err := execute("", "curl", "--fail", "--silent", "--show-error", "--max-time", "3", "http://main:3000/api/healthz")
			var health struct {
				Status string `json:"status"`
			}
			if err == nil && json.Unmarshal([]byte(result), &health) == nil && health.Status == "pass" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("Gitea did not become healthy after setup/restart")
			}
			time.Sleep(time.Second)
		}
		if _, err = execute("", "sh", "-c", `test "$GITEA_APP_INI" = /var/lib/gitea/custom/conf/app.ini && grep -Eq '^INSTALL_LOCK[[:space:]]*=[[:space:]]*true[[:space:]]*$' "$GITEA_APP_INI"`); err != nil {
			t.Fatal("completed Gitea setup is not locked in persistent app.ini")
		}
		curlConfig := "url = \"http://main:3000/api/v1/user\"\nuser = \"catalogadmin:" + password + "\"\n"
		result, err := execute(curlConfig, "curl", "--fail", "--silent", "--show-error", "--config", "-")
		var user struct {
			Login string `json:"login"`
			Admin bool   `json:"is_admin"`
		}
		if err != nil || json.Unmarshal([]byte(result), &user) != nil || user.Login != "catalogadmin" || !user.Admin {
			t.Fatal("persistent Gitea administrator could not authenticate")
		}
	}
	verify()
	claim, err := client.kube.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, "main-data", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service := target.Spec.Services["main"]
	service.RestartNonce = "catalog-persistence-check"
	target.Spec.Services["main"] = service
	target.Revision++
	target.OperationID = "catalog-restart"
	if _, err = client.Deploy(ctx, *target, nil); err != nil {
		t.Fatal("Gitea restart failed", err)
	}
	verify()
	next, err := client.kube.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, "main-data", metav1.GetOptions{})
	if err != nil || next.UID != claim.UID {
		t.Fatal("Gitea restart replaced its PVC")
	}
	t.Log("Gitea HTTP setup completed; persistent app.ini remains locked and administrator API authentication survives restart on the same PVC")
}

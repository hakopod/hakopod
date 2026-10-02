//go:build hakopod_native_acceptance && linux

package nativeacceptance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func validConfiguration(now time.Time) configuration {
	c := configuration{SchemaVersion: 1, RunID: strings.Repeat("a", 32), Kind: "neon", Project: "native-neon", Environment: "development", Context: "k3d-hakopod-dev", ClusterUID: "cluster-uid", NodeUIDs: map[string]string{"server": "uid-1", "worker": "uid-2", "third": "uid-3"}, Kubeconfig: "/protected/kubeconfig", SourceRoot: "/protected/source", SourceFiles: map[string]string{}, BinarySHA256: strings.Repeat("b", 64), ExpiresAt: now.Add(time.Hour)}
	for _, name := range []string{"go.mod", "go.sum", "cmd/main.go", "internal/one.go", "internal/two.go", "internal/three.go", "internal/four.go", "internal/five.go", "internal/six.go", "internal/seven.go"} {
		c.SourceFiles[name] = strings.Repeat("c", 64)
	}
	return c
}

func TestAcceptanceScopeAndExpiry(t *testing.T) {
	now := time.Now()
	c := validConfiguration(now)
	if err := validate(c, now); err != nil {
		t.Fatal(err)
	}
	active.Store(&c)
	defer active.Store(nil)
	plan := managedplatform.Plan{Capability: managedplatform.Capability{Reason: "unqualified"}}
	got := Plan(c.Project, c.Environment, c.Kind, plan)
	if !got.Capability.Available || !got.Capability.ClusterQualified || got.Capability.PublicQualified {
		t.Fatal("development plan has incorrect capability")
	}
	for _, scope := range [][3]string{{"other", c.Environment, c.Kind}, {c.Project, "production", c.Kind}, {c.Project, c.Environment, "supabase"}} {
		if got := Plan(scope[0], scope[1], scope[2], plan); got.Capability != plan.Capability {
			t.Fatal("qualification escaped configured scope")
		}
	}
	c.ExpiresAt = now.Add(-time.Second)
	if got := Plan(c.Project, c.Environment, c.Kind, plan); got.Capability != plan.Capability {
		t.Fatal("expired qualification remained enabled")
	}
}

func TestAcceptanceConfigurationRefusesInvalidBounds(t *testing.T) {
	now := time.Now()
	for name, mutate := range map[string]func(*configuration){
		"operator context":   func(c *configuration) { c.Context = "operator" },
		"customer project":   func(c *configuration) { c.Project = "customer" },
		"production":         func(c *configuration) { c.Environment = "production" },
		"missing third node": func(c *configuration) { delete(c.NodeUIDs, "third") },
		"repeated node uid":  func(c *configuration) { c.NodeUIDs["third"] = c.NodeUIDs["worker"] },
		"expired":            func(c *configuration) { c.ExpiresAt = now },
		"unbounded duration": func(c *configuration) { c.ExpiresAt = now.Add(5 * time.Hour) },
		"source traversal":   func(c *configuration) { c.SourceFiles["../other"] = strings.Repeat("c", 64) },
		"unknown kind":       func(c *configuration) { c.Kind = "oracle" },
	} {
		t.Run(name, func(t *testing.T) {
			c := validConfiguration(now)
			mutate(&c)
			if validate(c, now) == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestAcceptanceRejectsNestedDuplicateKeys(t *testing.T) {
	raw, err := json.Marshal(validConfiguration(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	for _, mapName := range []string{"node_uids", "source_files"} {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		fields[mapName] = json.RawMessage(`{"repeated":"a","repeated":"b"}`)
		ambiguous, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeConfiguration(strings.NewReader(string(ambiguous))); err == nil {
			t.Fatal("nested duplicate accepted", mapName)
		}
	}
}

func TestAcceptanceStopsAndClosesGateOnIdentityDrift(t *testing.T) {
	c := validConfiguration(time.Now())
	active.Store(&c)
	defer active.Store(nil)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go watch(ctx, stop, time.Millisecond, func(context.Context, configuration) error { return errors.New("identity changed") })
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("identity drift did not stop process")
	}
	if Recovery(c.Project, c.Environment, c.Kind) {
		t.Fatal("identity drift left gate open")
	}
}

func TestAcceptanceGuardDetectsFileAndClusterDrift(t *testing.T) {
	c := validConfiguration(time.Now())
	c.SourceRoot = t.TempDir()
	c.SourceFiles = map[string]string{}
	path := filepath.Join(c.SourceRoot, "source.go")
	if err := os.WriteFile(path, []byte("package example"), 0600); err != nil {
		t.Fatal(err)
	}
	_, identity, err := readIdentity(path, 64, true)
	if err != nil {
		t.Fatal(err)
	}
	c.SourceFiles["source.go"] = identity.digest
	kube := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID(c.ClusterUID)}})
	for name, uid := range c.NodeUIDs {
		if _, err := kube.CoreV1().Nodes().Create(context.Background(), &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(uid)}}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	c.guard = &runGuard{files: []fileIdentity{identity, identity, identity, identity}, kube: kube}
	if err := verifyRun(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	node, err := kube.CoreV1().Nodes().Get(context.Background(), "third", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.UID = "replaced"
	if _, err = kube.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if verifyRun(context.Background(), c) == nil {
		t.Fatal("node replacement accepted")
	}
	if err := os.WriteFile(path, []byte("package changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if identity.verify() == nil {
		t.Fatal("changed file accepted")
	}
	if err := os.WriteFile(path, []byte("package example"), 0600); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(c.SourceRoot, "replacement")
	if err := os.WriteFile(replacement, []byte("package example"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if identity.verify() == nil {
		t.Fatal("same-content replacement accepted")
	}
}

func TestAcceptanceProtectedFileAndSourceMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input")
	if err := os.WriteFile(path, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := protected(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if f, err = protected(path, 4); err == nil {
		f.Close()
		t.Fatal("public config accepted")
	}
	if _, err = hashFile(path, 4); err != nil {
		t.Fatal("read-only source rejected", err)
	}
	if err = os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err = hashFile(path, 4); err == nil {
		t.Fatal("world-writable source accepted")
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if f, err = protected(link, 4); err == nil {
		f.Close()
		t.Fatal("symbolic config accepted")
	}
	if f, err = protected(path, 3); err == nil {
		f.Close()
		t.Fatal("oversized config accepted")
	}
}

func TestAcceptanceExpiryStopsProcess(t *testing.T) {
	c := validConfiguration(time.Now())
	c.ExpiresAt = time.Now().Add(10 * time.Millisecond)
	active.Store(&c)
	defer active.Store(nil)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go Watch(ctx, stop)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("acceptance did not stop at expiry")
	}
}

func TestAcceptanceSourceInventoryIsExact(t *testing.T) {
	c := validConfiguration(time.Now())
	c.SourceRoot = t.TempDir()
	c.SourceFiles = map[string]string{}
	path := filepath.Join(c.SourceRoot, "source.go")
	if err := os.WriteFile(path, []byte("package example"), 0644); err != nil {
		t.Fatal(err)
	}
	digest, err := hashFile(path, 64)
	if err != nil {
		t.Fatal(err)
	}
	c.SourceFiles["source.go"] = digest
	if err = verifySources(c); err != nil {
		t.Fatal(err)
	}
	c.SourceFiles["missing.go"] = digest
	if verifySources(c) == nil {
		t.Fatal("missing source accepted")
	}
	delete(c.SourceFiles, "missing.go")
	if err = os.WriteFile(filepath.Join(c.SourceRoot, "extra.go"), []byte("extra"), 0644); err != nil {
		t.Fatal(err)
	}
	if verifySources(c) == nil {
		t.Fatal("extra source accepted")
	}
}

func TestAcceptanceConfigurationRejectsDuplicateAndUnknownFields(t *testing.T) {
	c := validConfiguration(time.Now())
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeConfiguration(strings.NewReader(string(raw))); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{`{"schema_version":1,`, `{"unknown":true,`} {
		if _, err = decodeConfiguration(strings.NewReader(prefix + string(raw[1:]))); err == nil {
			t.Fatal("ambiguous configuration accepted")
		}
	}
}

package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type secretReadFixture struct {
	namespaceCalls  atomic.Int32
	reads           atomic.Int32
	active          atomic.Int32
	peak            atomic.Int32
	mu              sync.Mutex
	names           []string
	delay           time.Duration
	namespaceStatus int
	secretStatus    int
	foreign         bool
	corruptLabel    string
	missing         map[string]bool
	empty           map[string]bool
	entered         chan struct{}
	exited          atomic.Int32
	hold            bool
}

func (f *secretReadFixture) client(t *testing.T) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(500)
			return
		}
		if r.URL.Path == "/api/v1/namespaces/"+PlatformNamespace {
			f.namespaceCalls.Add(1)
			if f.namespaceStatus != 0 {
				w.WriteHeader(f.namespaceStatus)
				fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","reason":"NotFound","message":"backend-private-value"}`)
				return
			}
			json.NewEncoder(w).Encode(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: PlatformNamespace, Labels: map[string]string{managedBy: "hakopod"}}})
			return
		}
		prefix := "/api/v1/namespaces/" + PlatformNamespace + "/secrets/"
		if !strings.HasPrefix(r.URL.Path, prefix) || r.URL.RawQuery != "" {
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, prefix)
		if !strings.HasPrefix(name, workloadSecretName("one", "main", "app", "")) {
			t.Errorf("foreign request %s", name)
			w.WriteHeader(500)
			return
		}
		f.reads.Add(1)
		active := f.active.Add(1)
		for peak := f.peak.Load(); active > peak; peak = f.peak.Load() {
			if f.peak.CompareAndSwap(peak, active) {
				break
			}
		}
		defer f.active.Add(-1)
		defer f.exited.Add(1)
		f.mu.Lock()
		f.names = append(f.names, name)
		f.mu.Unlock()
		if f.entered != nil {
			f.entered <- struct{}{}
		}
		if f.hold {
			<-r.Context().Done()
			return
		}
		if f.delay > 0 {
			select {
			case <-time.After(f.delay):
			case <-r.Context().Done():
				return
			}
		}
		if f.secretStatus != 0 {
			w.WriteHeader(f.secretStatus)
			fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","reason":"InternalError","message":"backend-private-value"}`)
			return
		}
		ref := strings.TrimPrefix(name, workloadSecretName("one", "main", "app", ""))
		if f.missing[ref] {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","reason":"NotFound"}`)
			return
		}
		scope := secretScope("one", "main", "app")
		if f.foreign {
			scope = secretScope("other", "main", "app")
		}
		labels := map[string]string{managedBy: "hakopod", platformSecretLabel: "true", "hakopod.io/secret-scope": scope, "hakopod.io/secret-name": ref}
		if f.corruptLabel != "" {
			labels[f.corruptLabel] = "foreign"
		}
		value := []byte("fixture-private-value")
		if f.empty[ref] {
			value = nil
		}
		json.NewEncoder(w).Encode(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Data: map[string][]byte{"value": value}})
	}))
	t.Cleanup(server.Close)
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL, QPS: 10, Burst: 20})
	if err != nil {
		t.Fatal(err)
	}
	return &Client{kube: kube}
}

func requirementsApp(count int) spec.Application {
	refs := map[string]spec.SecretRef{}
	for i := 0; i < count; i++ {
		refs[fmt.Sprintf("TOKEN%d", i)] = spec.SecretRef{Ref: fmt.Sprintf("token-%03d", i)}
	}
	return spec.Application{Name: "app", Services: map[string]spec.Service{"web": {Secrets: refs}}}
}

func TestMissingSecretsParallelExactReadsWithinCallerDeadline(t *testing.T) {
	f := &secretReadFixture{delay: 60 * time.Millisecond}
	c := f.client(t)
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	start := time.Now()
	missing, err := c.MissingWorkloadSecrets(ctx, "one", "main", requirementsApp(21))
	if err != nil || len(missing) != 0 {
		t.Fatalf("failed: %v %v", missing, err)
	}
	f.mu.Lock()
	names := append([]string(nil), f.names...)
	f.mu.Unlock()
	sort.Strings(names)
	expected := make([]string, 21)
	for i := range expected {
		expected[i] = workloadSecretName("one", "main", "app", fmt.Sprintf("token-%03d", i))
	}
	if !reflect.DeepEqual(names, expected) {
		t.Fatal("reads differ from the exact requested names")
	}
	if f.namespaceCalls.Load() != 1 || f.reads.Load() != 21 {
		t.Fatalf("requests namespace=%d secret=%d", f.namespaceCalls.Load(), f.reads.Load())
	}
	if f.peak.Load() != 4 || f.active.Load() != 0 {
		t.Fatalf("workers peak=%d active=%d", f.peak.Load(), f.active.Load())
	}
	if time.Since(start) > time.Second {
		t.Fatal("parallel request budget exceeded")
	}
}

func TestMissingSecretsCancellationJoinsReaders(t *testing.T) {
	f := &secretReadFixture{hold: true, entered: make(chan struct{}, 4)}
	c := f.client(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		missing, err := c.MissingWorkloadSecrets(ctx, "one", "main", requirementsApp(30))
		if missing != nil {
			result <- fmt.Errorf("partial result returned")
			return
		}
		result <- err
	}()
	for i := 0; i < 4; i++ {
		select {
		case <-f.entered:
		case <-time.After(time.Second):
			t.Fatal("readers did not start")
		}
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancellation succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("readers were not joined")
	}
	deadline := time.Now().Add(time.Second)
	for f.active.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if f.reads.Load() != 4 || f.active.Load() != 0 {
		t.Fatalf("readers started=%d active=%d", f.reads.Load(), f.active.Load())
	}
}

func TestMissingSecretsFailsClosedWithoutPrivateErrorValues(t *testing.T) {
	for _, fixture := range []*secretReadFixture{{namespaceStatus: 404}, {namespaceStatus: 503}, {secretStatus: 503}, {foreign: true}, {corruptLabel: managedBy}, {corruptLabel: platformSecretLabel}, {corruptLabel: "hakopod.io/secret-name"}} {
		f := fixture
		c := f.client(t)
		missing, err := c.MissingWorkloadSecrets(context.Background(), "one", "main", requirementsApp(21))
		if err == nil || missing != nil {
			t.Fatalf("invalid success missing=%v error=%v", missing, err)
		}
		if strings.Contains(err.Error(), "private-value") {
			t.Fatal("private error content escaped")
		}
		if f.namespaceStatus != 0 && f.reads.Load() != 0 {
			t.Fatal("secret read before namespace validation")
		}
		deadline := time.Now().Add(time.Second)
		for f.active.Load() != 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if f.active.Load() != 0 {
			t.Fatal("active readers after failure")
		}
	}
}

func TestMissingSecretsEmptyInputDoesNotReadNamespace(t *testing.T) {
	f := &secretReadFixture{}
	c := f.client(t)
	missing, err := c.MissingWorkloadSecrets(context.Background(), "one", "main", requirementsApp(0))
	if err != nil || len(missing) != 0 || f.namespaceCalls.Load() != 0 || f.reads.Load() != 0 {
		t.Fatal("empty requirements accessed storage")
	}
}

func TestMissingSecretsDeduplicatesAndSortsMixedResults(t *testing.T) {
	f := &secretReadFixture{missing: map[string]bool{"token-004": true, "token-001": true}, empty: map[string]bool{"token-003": true}}
	c := f.client(t)
	app := requirementsApp(5)
	app.Services["web"].Secrets["DUPLICATE"] = spec.SecretRef{Ref: "token-001"}
	missing, err := c.MissingWorkloadSecrets(context.Background(), "one", "main", app)
	if err != nil || !reflect.DeepEqual(missing, []string{"token-001", "token-003", "token-004"}) {
		t.Fatalf("mixed result %v: %v", missing, err)
	}
	if f.reads.Load() != 5 {
		t.Fatal("duplicate reference was read again")
	}
}

func TestMissingSecretsRejectsOversizedInputBeforeStorage(t *testing.T) {
	f := &secretReadFixture{}
	c := f.client(t)
	missing, err := c.MissingWorkloadSecrets(context.Background(), "one", "main", requirementsApp(101))
	if err == nil || missing != nil || f.namespaceCalls.Load() != 0 || f.reads.Load() != 0 {
		t.Fatal("oversized requirements accessed storage")
	}
}

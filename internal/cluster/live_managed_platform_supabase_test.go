package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestSupabaseLiveRefusesUnclaimedNamespace(t *testing.T) {
	if os.Getenv("HAKOPOD_SUPABASE_TEST") != "1" {
		t.Skip("set HAKOPOD_SUPABASE_TEST=1 for named development cluster acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("Supabase acceptance requires k3d-hakopod-dev")
	}
	client, err := New(path, developmentDatabaseOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	if _, err = rand.Read(id); err != nil {
		t.Fatal(err)
	}
	op := store.ManagedPlatformOperation{ID: "hakopod-test-ownership", PlatformID: hex.EncodeToString(id), Revision: 1, Kind: "create", Lease: "hakopod-test-lease"}
	name := "managed-platform-" + op.PlatformID
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	namespace, err := client.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"app.kubernetes.io/managed-by": "hakopod", "hakopod.io/managed-platform-id": op.PlatformID, "hakopod.io/owner-operation-id": op.ID}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		uid := namespace.UID
		if deleteErr := client.kube.CoreV1().Namespaces().Delete(cleanup, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
			t.Errorf("delete hostile Supabase namespace: %v", deleteErr)
			return
		}
		for {
			_, getErr := client.kube.CoreV1().Namespaces().Get(cleanup, name, metav1.GetOptions{})
			if apierrors.IsNotFound(getErr) {
				return
			}
			if getErr != nil {
				t.Errorf("verify hostile Supabase namespace cleanup: %v", getErr)
				return
			}
			select {
			case <-cleanup.Done():
				t.Errorf("hostile Supabase namespace cleanup timed out: %v", cleanup.Err())
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	})
	state := newFakeSupabaseStore()
	if _, err = client.ensureSupabaseNamespace(ctx, state, op, map[string]store.PlatformResourceClaim{}, map[string]store.PlatformResourceClaim{}, func() error { return nil }); err == nil {
		t.Fatal("real unclaimed namespace was adopted")
	}
	items, err := client.kube.CoreV1().Pods(name).List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(items.Items) != 0 {
		t.Fatal("ownership refusal mutated the foreign namespace")
	}
}

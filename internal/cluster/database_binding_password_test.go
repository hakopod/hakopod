package cluster

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestManagedBindingPasswordSnapshotIsScopedAndEscaped(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "external"}[external], func(t *testing.T) {
			ctx := context.Background()
			target := testTarget(t)
			ref := spec.SecretRef{Ref: "database-password"}
			if external {
				ref = spec.SecretRef{Provider: "vault", Path: "database", Key: "password"}
			}
			svc := target.Spec.Services["api"]
			svc.Bindings = map[string]spec.Binding{"DATABASE_URL": {ManagedDatabase: strings.Repeat("a", 32), Protocol: "postgres", Endpoint: "read_write", Username: "tenant", Database: "restored", Password: &ref}}
			target.Spec.Services["api"] = svc
			password := []byte("development-fixture:p@ss:/?#%")
			client := &Client{kube: fake.NewClientset()}
			if external {
				client.SetExternalSecretResolver(func(_ context.Context, project, environment string, app spec.Application) (map[string]map[string][]byte, error) {
					if project != target.Project || environment != target.Environment || app.Name != target.Spec.Name {
						t.Fatal("external password escaped its application scope")
					}
					return map[string]map[string][]byte{"api": {spec.BindingSecretKey("DATABASE_URL"): password}}, nil
				})
			} else {
				if err := client.PutWorkloadSecret(ctx, target.Project, target.Environment, target.Spec.Name, ref.Ref, string(password)); err != nil {
					t.Fatal(err)
				}
				if err := client.PutWorkloadSecret(ctx, target.Project, target.Environment, "another-app", ref.Ref, "foreign-value"); err != nil {
					t.Fatal(err)
				}
				if missing, err := client.MissingWorkloadSecrets(ctx, target.Project, target.Environment, target.Spec); err != nil || len(missing) != 0 {
					t.Fatal("saved binding password was not discovered", err)
				}
			}
			client.SetDatabaseBindingResolver(func(_ context.Context, project, environment string, app spec.Application) (map[string]map[string]DatabaseConnection, error) {
				if target.secretValues != nil {
					t.Fatal("caller target mutated before snapshot completion")
				}
				return map[string]map[string]DatabaseConnection{"api": {"DATABASE_URL": {URL: "postgres://tenant:@database.internal:5432/restored?sslmode=verify-full", Port: 5432}}}, nil
			})
			selected := target
			if err := client.snapshotDatabaseBindings(ctx, &selected); err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(selected.databaseConnections["api"]["DATABASE_URL"].URL)
			if err != nil {
				t.Fatal("invalid URI")
			}
			got, _ := u.User.Password()
			if got != string(password) || u.User.Username() != "tenant" || u.Path != "/restored" || u.Query().Get("sslmode") != "verify-full" {
				t.Fatal("binding password or selected target lost")
			}
			if err = client.prepareWorkloadSecrets(ctx, selected, "api", svc); err != nil {
				t.Fatal(err)
			}
			saved, err := client.kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Get(ctx, "api-environment", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(saved.Data) != 1 || string(saved.Data["DATABASE_URL"]) != u.String() {
				t.Fatal("only the connection URL should enter the application Secret")
			}
		})
	}
}

func TestManagedBindingMissingPasswordCannotUseManagedCredentials(t *testing.T) {
	ctx := context.Background()
	target := testTarget(t)
	svc := target.Spec.Services["api"]
	svc.Bindings = map[string]spec.Binding{"DATABASE_URL": {ManagedDatabase: strings.Repeat("a", 32), Protocol: "postgres", Endpoint: "read_write", Username: "tenant", Password: &spec.SecretRef{Ref: "database-password"}}}
	target.Spec.Services["api"] = svc
	client := &Client{kube: fake.NewClientset()}
	if err := client.PutWorkloadSecret(ctx, target.Project, target.Environment, "another-app", "database-password", "foreign-value"); err != nil {
		t.Fatal(err)
	}
	missing, err := client.MissingWorkloadSecrets(ctx, target.Project, target.Environment, target.Spec)
	if err != nil || !reflect.DeepEqual(missing, []string{"database-password"}) {
		t.Fatal("missing binding password did not respect application scope", err)
	}
	called := false
	client.SetDatabaseBindingResolver(func(context.Context, string, string, spec.Application) (map[string]map[string]DatabaseConnection, error) {
		called = true
		return nil, errors.New("resolver must not run")
	})
	before := len(client.kube.(*fake.Clientset).Actions())
	if err = client.snapshotDatabaseBindings(ctx, &target); err == nil || called || target.databaseConnections != nil {
		t.Fatal("missing custom password fell back to managed credentials")
	}
	for _, action := range client.kube.(*fake.Clientset).Actions()[before:] {
		if action.GetVerb() != "get" && action.GetVerb() != "list" {
			t.Fatal("failed snapshot mutated Kubernetes")
		}
	}
}

func TestManagedBindingPasswordSnapshotNeverLeaksValuesOnFailure(t *testing.T) {
	for _, value := range []string{"", strings.Repeat("@", 4097)} {
		t.Run(map[bool]string{true: "empty", false: "oversized"}[value == ""], func(t *testing.T) {
			target := testTarget(t)
			svc := target.Spec.Services["api"]
			svc.Bindings = map[string]spec.Binding{"DATABASE_URL": {ManagedDatabase: strings.Repeat("a", 32), Protocol: "postgres", Endpoint: "read_write", Password: &spec.SecretRef{Ref: "database-password"}}}
			target.Spec.Services["api"] = svc
			target.secretValues = map[string]map[string][]byte{"api": {spec.BindingSecretKey("DATABASE_URL"): []byte(value)}}
			client := &Client{options: Options{DatabaseBindings: func(context.Context, string, string, spec.Application) (map[string]map[string]DatabaseConnection, error) {
				return map[string]map[string]DatabaseConnection{"api": {"DATABASE_URL": {URL: "postgres://app:@database.internal:5432/app", Port: 5432}}}, nil
			}}}
			err := client.snapshotDatabaseBindings(context.Background(), &target)
			if err == nil || value != "" && strings.Contains(err.Error(), value) || target.databaseConnections != nil {
				t.Fatal("invalid password leaked or produced a connection")
			}
		})
	}
}

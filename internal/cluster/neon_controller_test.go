package cluster

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
)

func TestNeonControllerRuntimeSecretEscapesCredentialsAndScopesToken(t *testing.T) {
	platform := strings.Repeat("a", 32)
	key := []byte(strings.Repeat("k", 32))
	password := "special:/?@&+ password"
	request := NeonRuntimeRequest{Operation: store.ManagedPlatformOperation{PlatformID: platform, Revision: 3}, Render: managedplatform.NeonRenderInput{Spec: managedplatform.Spec{Secrets: map[string]managedplatform.SecretReference{"controller-database-password": {Name: "controller-db", Revision: 2}}}}, SecretSnapshots: map[string]map[string][]byte{"controller-db-r2": {"value": []byte(password)}}}
	if err := prepareNeonControllerSecret(&request, key); err != nil {
		t.Fatal(err)
	}
	secret := request.SecretSnapshots[managedplatform.NeonControllerCallbackSecretName(3)]
	u, err := url.Parse(string(secret["database-url"]))
	if err != nil {
		t.Fatal("database URI rejected")
	}
	actual, ok := u.User.Password()
	if !ok || actual != password || u.User.Username() != "storage_controller" || u.Host != "neon-controller-database.managed-platform-"+platform+".svc:5432" || u.Query().Get("sslmode") != "require" {
		t.Fatal("controller database URI changed credentials or trust mode")
	}
	if !managedplatform.ValidNeonControllerToken(key, platform, 3, string(secret["token"])) || managedplatform.ValidNeonControllerToken(key, platform, 2, string(secret["token"])) {
		t.Fatal("callback token scope changed")
	}
	if err = prepareNeonControllerSecret(&request, key); err == nil {
		t.Fatal("reserved synthetic secret was replaced")
	}
}

func TestNeonControllerRejectsStaleGenerationAndResetsRestoredIdentity(t *testing.T) {
	tenant, timeline := strings.Repeat("a", 32), strings.Repeat("b", 32)
	old := managedplatform.NeonControllerState{Attach: &managedplatform.NeonAttachNotification{TenantID: tenant, Shards: []managedplatform.NeonAttachShard{{NodeID: 1}}}, Safekeepers: &managedplatform.NeonSafekeeperNotification{TenantID: tenant, TimelineID: timeline, Generation: 8, Safekeepers: []managedplatform.NeonSafekeeperMember{{ID: 1}, {ID: 2}, {ID: 3}}}}
	stale := *old.Safekeepers
	stale.Generation = 7
	if _, err := mergeNeonControllerState(old, tenant, timeline, 2, nil, &stale); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale membership generation accepted")
	}
	foreign := *old.Safekeepers
	foreign.TenantID = strings.Repeat("c", 32)
	if _, err := mergeNeonControllerState(old, tenant, timeline, 2, nil, &foreign); !errors.Is(err, store.ErrInput) {
		t.Fatal("foreign tenant accepted")
	}
	restored := strings.Repeat("d", 32)
	attachment := &managedplatform.NeonAttachNotification{TenantID: restored, Shards: []managedplatform.NeonAttachShard{{NodeID: 2}}}
	next, err := mergeNeonControllerState(old, restored, timeline, 2, attachment, nil)
	if err != nil || next.Attach.TenantID != restored || next.Safekeepers != nil {
		t.Fatal("restore mixed old and new identities")
	}
}

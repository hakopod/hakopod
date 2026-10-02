package cluster

import (
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
)

func TestValidateNeonTLSSecretSnapshotsRequiresOwnedNamesAndSharedBrokerTrust(t *testing.T) {
	platformID := "0123456789abcdef0123456789abcdef"
	namespace := "managed-platform-" + platformID
	names := []string{
		"neon-broker", "neon-broker." + namespace + ".svc",
		"neon-storage-controller", "neon-storage-controller." + namespace + ".svc",
		"neon-controller-database", "neon-controller-database." + namespace + ".svc",
		"neon-proxy", "neon-proxy." + namespace + ".svc",
		"neon-pageserver-0", "neon-pageserver-0." + namespace + ".svc",
		"neon-pageserver-1", "neon-pageserver-1." + namespace + ".svc",
		"neon-safekeeper-0", "neon-safekeeper-0." + namespace + ".svc",
		"neon-safekeeper-1", "neon-safekeeper-1." + namespace + ".svc",
		"neon-safekeeper-2", "neon-safekeeper-2." + namespace + ".svc",
		"neon-compute-0-control", "neon-compute-0-control." + namespace + ".svc",
	}
	identity := databaseTLSFixture(t, names, false)
	spec := managedplatform.Spec{Neon: &managedplatform.NeonConfig{ComputeReplicas: 1, Pageservers: 2, Safekeepers: 3}, Secrets: map[string]managedplatform.SecretReference{}}
	values := map[string]map[string][]byte{}
	for _, logical := range []string{"broker-auth", "compute-auth", "controller-auth", "controller-database-password", "pageserver-auth", "proxy-auth", "safekeeper-auth"} {
		spec.Secrets[logical] = managedplatform.SecretReference{Name: logical, Revision: 1}
		values[logical+"-r1"] = map[string][]byte{"tls.crt": identity["tls.crt"], "tls.key": identity["tls.key"], "ca.crt": identity["ca.crt"]}
	}
	if err := validateNeonTLSSecretSnapshots(values, spec, platformID, time.Now()); err != nil {
		t.Fatal(err)
	}
	wrongName := databaseTLSFixture(t, []string{"wrong.internal"}, false)
	values["controller-database-password-r1"] = wrongName
	if err := validateNeonTLSSecretSnapshots(values, spec, platformID, time.Now()); err == nil {
		t.Fatal("controller database certificate without owned service names was accepted")
	}
	values["controller-database-password-r1"] = identity
	wrongCA := databaseTLSFixture(t, names, false)
	values["pageserver-auth-r1"] = wrongCA
	if err := validateNeonTLSSecretSnapshots(values, spec, platformID, time.Now()); err == nil {
		t.Fatal("broker certificate outside the pageserver trust bundle was accepted")
	}
	values["pageserver-auth-r1"] = identity
	values["broker-auth-r1"] = map[string][]byte{"tls.crt": identity["tls.crt"], "tls.key": wrongCA["tls.key"], "ca.crt": identity["ca.crt"]}
	if err := validateNeonTLSSecretSnapshots(values, spec, platformID, time.Now()); err == nil {
		t.Fatal("mismatched broker private key was accepted")
	}
}

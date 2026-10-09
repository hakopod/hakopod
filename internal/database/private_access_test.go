package database

import (
	"strings"
	"testing"
	"time"
)

func TestPrivateAccessHostPlacementAndTLS(t *testing.T) {
	now := time.Now().UTC()
	d := Resource{ID: strings.Repeat("a", 32), Revision: 2, Status: "ready", Spec: Spec{Engine: "postgresql", TLS: &TLSConfig{Mode: "required"}}, Observation: Observation{Revision: 2, ObservedAt: now, Status: "ready", Endpoints: []Endpoint{{Purpose: "read_write", Host: "database-rw.hdb-" + strings.Repeat("a", 32) + ".svc", Port: 5432}}}}
	for _, location := range []string{"local", "ssh", "kubernetes"} {
		g, err := PrivateAccess(d, PrivateAccessInput{Location: location, Endpoint: "read_write", SSHHost: "operator@example.test", KubeContext: "owned-context", LocalPort: 15432}, false, now)
		if err != nil || len(g.Blockers) != 0 {
			t.Fatal(location, err, g.Blockers)
		}
		var commands string
		for _, step := range g.Steps {
			commands += step.Command + "\n"
		}
		if !strings.Contains(commands, "PGSSLMODE=verify-full") || strings.Contains(commands, "sslmode=require") {
			t.Fatal("hostname verification missing", location)
		}
		if location == "kubernetes" {
			if strings.Contains(commands, "port-forward") || strings.Contains(commands, "PGHOSTADDR") {
				t.Fatal("cluster client given loopback route")
			}
		} else if !strings.Contains(commands, "PGHOSTADDR=127.0.0.1") || !strings.Contains(commands, "--address 127.0.0.1") {
			t.Fatal("remote client given unreachable service DNS")
		}
		if (location == "local") != strings.Contains(commands, "ssh -N") {
			t.Fatal("SSH hop does not match client location")
		}
	}
	cloud, _ := PrivateAccess(d, PrivateAccessInput{Location: "local", Endpoint: "read_write"}, true, now)
	if len(cloud.Blockers) == 0 || len(cloud.Steps) != 0 {
		t.Fatal("cloud customer given operator credentials path")
	}
	d.Observation.ObservedAt = now.Add(-3 * time.Minute)
	stale, _ := PrivateAccess(d, PrivateAccessInput{Location: "local", Endpoint: "read_write"}, false, now)
	if len(stale.Blockers) == 0 || len(stale.Steps) != 0 {
		t.Fatal("stale endpoint gave runnable commands")
	}
}

func TestPrivateAccessRejectsUntrustedCommandsAndDiscoveryTunnels(t *testing.T) {
	for _, in := range []PrivateAccessInput{
		{Location: "local", Endpoint: "read_write", SSHHost: "host -oProxyCommand=touch"},
		{Location: "local", Endpoint: "read_write", SSHHost: "$(touch /tmp/test)"},
		{Location: "ssh", Endpoint: "read_write", KubeContext: "-bad"},
		{Location: "ssh", Endpoint: "read_write", LocalPort: 22},
	} {
		if in.Validate() == nil {
			t.Fatal("unsafe input accepted")
		}
	}
	now := time.Now().UTC()
	for _, engine := range []string{"mongodb", "redis"} {
		d := Resource{ID: "fixture", Revision: 1, Status: "ready", Spec: Spec{Engine: engine, Mode: "cluster"}, Observation: Observation{Revision: 1, Status: "ready", ObservedAt: now, Endpoints: []Endpoint{{Purpose: "cluster", Host: "database.hdb-fixture.svc", Port: 27017}}}}
		g, err := PrivateAccess(d, PrivateAccessInput{Location: "local", Endpoint: "cluster", SSHHost: "host", KubeContext: "owned"}, false, now)
		if err != nil || len(g.Blockers) == 0 || len(g.Steps) != 0 {
			t.Fatal("single tunnel advertised for discovered members", engine, err)
		}
	}
}

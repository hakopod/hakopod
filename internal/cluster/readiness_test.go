package cluster

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	appsv1 "k8s.io/api/apps/v1"
)

const testReadinessImage = "example.org/hakopod/probe@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestReadinessDeploymentAndPreflight(t *testing.T) {
	for _, protocol := range []string{"tcp", "smtp", "smtp_starttls"} {
		for _, httpPath := range []string{"", "/health"} {
			t.Run(protocol+httpPath, func(t *testing.T) {
				r := &spec.Readiness{Protocol: protocol, Port: 2525}
				if protocol == "smtp_starttls" {
					r.TLSServerName = "mail.example.com"
				}
				app, err := spec.Normalize(spec.Application{Name: "mail", Services: map[string]spec.Service{"smtp": {Image: "example.org/mail:latest", Port: 2525, Healthcheck: httpPath, Readiness: r, ReadOnlyRootFilesystem: true, RunAsUser: 12345, RunAsGroup: 23456, FSGroup: 23456}}})
				if err != nil {
					t.Fatal(err)
				}
				target := Target{ApplicationID: "readiness", Spec: app}
				svc := app.Services["smtp"]
				c := &Client{}
				helper := spec.NeedsReadinessHelper(svc)
				if err := c.ValidateDelivery(context.Background(), target); (err != nil) != helper {
					t.Fatalf("preflight helper requirement: %v", err)
				}
				c.options.ReadinessProbeImage = testReadinessImage
				if err := c.ValidateReadiness(app); err != nil {
					t.Fatal(err)
				}
				pod := deployment(target, "smtp", svc, time.Minute, testReadinessImage).Spec.Template.Spec
				probe := pod.Containers[0].ReadinessProbe
				if !helper {
					if probe.TCPSocket == nil || probe.TCPSocket.Port.IntVal != 2525 || len(pod.InitContainers) != 0 {
						t.Fatal("native TCP probe unexpectedly requires helper")
					}
					return
				}
				if probe.Exec == nil || len(pod.InitContainers) != 1 || len(pod.Containers) != 1 || *pod.SecurityContext.FSGroup != 23456 {
					t.Fatal("invalid helper placement or security group")
				}
				init := pod.InitContainers[0]
				if init.Image != testReadinessImage || !*init.SecurityContext.ReadOnlyRootFilesystem || *init.SecurityContext.AllowPrivilegeEscalation || init.Resources.Limits.Memory().String() != "64Mi" {
					t.Fatal("helper image/security/resources not bounded")
				}
				mount := pod.Containers[0].VolumeMounts[len(pod.Containers[0].VolumeMounts)-1]
				if !mount.ReadOnly || mount.MountPath != spec.ReadinessHelperDirectory {
					t.Fatal("application can write helper")
				}
				if strings.Contains(strings.Join(probe.Exec.Command, " "), "--http-path=") != (httpPath != "") {
					t.Fatal("HTTP readiness not composed")
				}
				if pod.Containers[0].LivenessProbe != nil {
					t.Fatal("readiness failure must not introduce restart policy")
				}
			})
		}
	}
}

func TestReadinessImageValidation(t *testing.T) {
	for _, image := range []string{"example.org/probe:latest", "https://example.org/probe@sha256:" + strings.Repeat("a", 64), "user:password@example.org/probe@sha256:" + strings.Repeat("a", 64), testReadinessImage + "\n"} {
		if ValidateReadinessProbeImage(image) == nil {
			t.Fatalf("accepted unpinned or invalid image %q", image)
		}
	}
	if err := ValidateReadinessProbeImage(testReadinessImage); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateStrategyDeployment(t *testing.T) {
	for _, strategy := range []string{"", "rolling", "recreate"} {
		for _, persistent := range []bool{false, true} {
			svc := spec.Service{Image: "example.org/test:latest", Size: "small", Replicas: 1, UpdateStrategy: strategy}
			if persistent {
				svc.Volume = &spec.Volume{MountPath: "/data", SizeGiB: 1}
			}
			dep := deployment(Target{ApplicationID: "strategy"}, "test", svc, time.Minute)
			wantRecreate := persistent || strategy == "recreate"
			if (dep.Spec.Strategy.Type == appsv1.RecreateDeploymentStrategyType) != wantRecreate {
				t.Fatalf("strategy %q persistent=%v: %+v", strategy, persistent, dep.Spec.Strategy)
			}
			if wantRecreate && dep.Spec.Strategy.RollingUpdate != nil {
				t.Fatal("Recreate retained rolling update parameters")
			}
			if !wantRecreate && (dep.Spec.Strategy.RollingUpdate == nil || dep.Spec.Strategy.RollingUpdate.MaxSurge.IntVal != 1 || dep.Spec.Strategy.RollingUpdate.MaxUnavailable.IntVal != 0) {
				t.Fatal("default rolling strategy changed")
			}
		}
	}
}

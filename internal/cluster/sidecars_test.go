package cluster

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const testSidecarImage = "tailscale/tailscale@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func sidecarTarget() (Target, spec.Service) {
	svc := spec.Service{Image: "kelvo:1", Size: "small", Replicas: 1, NodeName: "kelvo-1", NodeProfile: "kelvo-node",
		TemporaryMounts: []spec.TemporaryMount{{MountPath: "/var/lib/ts", SizeMiB: 16}},
		Sidecars: []spec.Sidecar{
			{Name: "tailscale", Image: testSidecarImage, CPU: "100m", Memory: "128Mi", Env: map[string]string{"TS_USERSPACE": "false"},
				Secrets: map[string]spec.SecretRef{"TS_AUTHKEY": {Ref: "ts-key"}},
				Mounts:  []spec.SidecarMount{{Temporary: "/var/lib/ts", MountPath: "/var/lib/tailscale"}}},
			{Name: "logger", Image: testSidecarImage, CPU: "10m", Memory: "16Mi"},
		}}
	app := spec.Application{Name: "kelvo", Services: map[string]spec.Service{"node": svc}}
	return Target{ApplicationID: "kelvo-id", Project: "p", Environment: "e", Spec: app, secretValues: map[string]map[string][]byte{"node": {spec.SidecarSecretKey("tailscale", "TS_AUTHKEY"): []byte("tskey")}}}, svc
}

func TestSidecarsRenderAsRestrictedNativeSidecars(t *testing.T) {
	target, svc := sidecarTarget()
	pod := deployment(target, "node", svc, time.Minute).Spec.Template.Spec
	if len(pod.Containers) != 1 || len(pod.InitContainers) != 2 {
		t.Fatalf("containers %d init %d", len(pod.Containers), len(pod.InitContainers))
	}
	for _, c := range pod.InitContainers {
		sc := c.SecurityContext
		if c.RestartPolicy == nil || *c.RestartPolicy != corev1.ContainerRestartPolicyAlways {
			t.Fatalf("%s is not a native sidecar", c.Name)
		}
		if *sc.AllowPrivilegeEscalation || !*sc.RunAsNonRoot || *sc.RunAsUser != 10001 || len(sc.Capabilities.Add) != 0 || sc.Capabilities.Drop[0] != "ALL" || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault || sc.Privileged != nil {
			t.Fatalf("%s is not restricted: %+v", c.Name, sc)
		}
		if c.Resources.Requests.Cpu().Cmp(*c.Resources.Limits.Cpu()) != 0 || c.Resources.Requests.Memory().Cmp(*c.Resources.Limits.Memory()) != 0 {
			t.Fatalf("%s request differs from limit", c.Name)
		}
	}
	ts := pod.InitContainers[0]
	if len(ts.Env) != 2 || ts.Env[1].Name != "TS_AUTHKEY" || ts.Env[1].ValueFrom.SecretKeyRef == nil || ts.Env[1].Value != "" {
		t.Fatalf("env %+v", ts.Env)
	}
	if len(ts.VolumeMounts) != 1 || ts.VolumeMounts[0].Name != "temporary-0" || ts.VolumeMounts[0].MountPath != "/var/lib/tailscale" {
		t.Fatalf("mounts %+v", ts.VolumeMounts)
	}
	secret := sidecarSecret(target, "node", svc)
	if secret == nil || secret.Immutable == nil || string(secret.Data[spec.SidecarSecretKey("tailscale", "TS_AUTHKEY")]) != "tskey" || ts.Env[1].ValueFrom.SecretKeyRef.Name != secret.Name {
		t.Fatal("sidecar secret snapshot is wrong")
	}
}

func TestSidecarPrivilegesComeOnlyFromTheBinding(t *testing.T) {
	target, svc := sidecarTarget()
	b := nodeProfileBinding()
	b.Sidecars = map[string]SidecarGrant{"tailscale": {Capabilities: []string{"NET_ADMIN", "NET_RAW"}, TUN: true, RunAsRoot: true}}
	d := deployment(target, "node", svc, time.Minute)
	applyNodeProfile(b, &d.Spec)
	applySidecarGrants(b, &d.Spec.Template.Spec)
	pod := d.Spec.Template.Spec
	ts, logger := pod.InitContainers[0].SecurityContext, pod.InitContainers[1].SecurityContext
	if len(ts.Capabilities.Add) != 2 || *ts.RunAsUser != 0 || *ts.RunAsNonRoot || ts.Capabilities.Drop[0] != "ALL" || *ts.AllowPrivilegeEscalation {
		t.Fatalf("grant not applied: %+v", ts)
	}
	if len(logger.Capabilities.Add) != 0 || *logger.RunAsUser != 10001 || !*logger.RunAsNonRoot || len(pod.InitContainers[1].VolumeMounts) != 0 {
		t.Fatal("an ungranted sidecar was widened")
	}
	tun := false
	for _, v := range pod.Volumes {
		if v.HostPath != nil && v.HostPath.Path == "/dev/net/tun" && *v.HostPath.Type == corev1.HostPathCharDev {
			tun = true
		}
	}
	if !tun || pod.InitContainers[0].VolumeMounts[len(pod.InitContainers[0].VolumeMounts)-1].MountPath != "/dev/net/tun" {
		t.Fatal("tun device missing")
	}
	// Without a sidecar entry, or with the entry naming another sidecar, nothing changes.
	for _, grants := range []map[string]SidecarGrant{nil, {"other": {TUN: true, RunAsRoot: true}}} {
		plain := deployment(target, "node", svc, time.Minute)
		nb := nodeProfileBinding()
		nb.Sidecars = grants
		applySidecarGrants(nb, &plain.Spec.Template.Spec)
		for _, c := range plain.Spec.Template.Spec.InitContainers {
			if len(c.SecurityContext.Capabilities.Add) != 0 || *c.SecurityContext.RunAsUser != 10001 || len(c.VolumeMounts) > 1 {
				t.Fatal("unmatched sidecar was widened")
			}
		}
		for _, v := range plain.Spec.Template.Spec.Volumes {
			if v.HostPath != nil {
				t.Fatal("hostPath without a grant")
			}
		}
	}
}

func TestNodeProfileRunAsRootAppliesToTheServiceContainerOnly(t *testing.T) {
	target, svc := sidecarTarget()
	plain := deployment(target, "node", svc, time.Minute)
	if plain.Spec.Template.Spec.Containers[0].SecurityContext.RunAsUser != nil {
		t.Fatal("main container run-as changed without a grant")
	}
	b := nodeProfileBinding()
	b.RunAsRoot = true
	d := deployment(target, "node", svc, time.Minute)
	applyNodeProfile(b, &d.Spec)
	pod := d.Spec.Template.Spec
	main := pod.Containers[0].SecurityContext
	if *main.RunAsUser != 0 || *main.RunAsNonRoot {
		t.Fatalf("main container not root: %+v", main)
	}
	if !*pod.SecurityContext.RunAsNonRoot || *pod.SecurityContext.RunAsUser != 10001 {
		t.Fatal("pod default changed")
	}
	for _, c := range pod.InitContainers {
		if *c.SecurityContext.RunAsUser != 10001 || !*c.SecurityContext.RunAsNonRoot {
			t.Fatal("sidecar followed the main container to root")
		}
	}
}

func TestSidecarsCountInCapacityAndQuota(t *testing.T) {
	target, svc := sidecarTarget()
	cpu, memory := scheduledPodRequests(svc, nil)
	if cpu.MilliValue() != 120+100+10 || memory.Cmp(resource.MustParse("154Mi")) <= 0 {
		t.Fatal(cpu.MilliValue(), memory.String())
	}
	if got := memory.Value(); got != (154+128+16)<<20 {
		t.Fatal(got)
	}
	quota := &corev1.ResourceQuota{Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{}}}
	serviceResourceQuota(quota, target)
	// Two replicas worth for rollout: (600+100+10)m limit x 2.
	if q := quota.Spec.Hard[corev1.ResourceLimitsCPU]; q.MilliValue() != 2*(600+100+10) {
		t.Fatal(q.String())
	}
	// The scheduler view of the rendered pod agrees with the planned figure.
	d := deployment(target, "node", svc, time.Minute)
	podCPU, podMemory := podRequests(d.Spec.Template.Spec)
	if podCPU != cpu.MilliValue() || podMemory != memory.Value() {
		t.Fatal(podCPU, podMemory)
	}
}

func TestNodeProfileSidecarGrantsAreStrict(t *testing.T) {
	dir := t.TempDir()
	good := "schema_version=1\n[[bindings]]\nname='kelvo-node'\nproject='p'\nenvironment='e'\napplication='kelvo'\nservice='node'\nnode='kelvo-1'\nseccomp='unconfined'\nprivileged=true\nrun_as_root=true\n[bindings.sidecars.tailscale]\ncapabilities=['NET_ADMIN','NET_RAW']\ntun=true\nrun_as_root=true\n"
	read := func(body string) ([]NodeProfileBinding, error) {
		p := filepath.Join(dir, "bindings.toml")
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		return ReadNodeProfileBindingsFile(p)
	}
	got, err := read(good)
	if err != nil || !got[0].RunAsRoot || !got[0].Sidecars["tailscale"].TUN || len(got[0].Sidecars["tailscale"].Capabilities) != 2 {
		t.Fatal(err, got)
	}
	for name, body := range map[string]string{
		"unknown sidecar key": good + "privileged=true\n",
		"unknown binding key": strings.Replace(good, "run_as_root=true\n[", "run_as_root=true\nroot=true\n[", 1),
		"SYS_ADMIN":           strings.Replace(good, "'NET_RAW'", "'SYS_ADMIN'", 1),
		"repeat":              strings.Replace(good, "'NET_RAW'", "'NET_ADMIN'", 1),
		"bad sidecar name":    strings.Replace(good, "sidecars.tailscale", "sidecars.Tail_Scale", 1),
		"string tun":          strings.Replace(good, "tun=true", "tun='yes'", 1),
	} {
		if _, err := read(body); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

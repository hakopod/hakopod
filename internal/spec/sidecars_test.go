package spec

import (
	"strings"
	"testing"
)

const sidecarDigest = "tailscale/tailscale@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func sidecarApp(extra string) string {
	return "name='kelvo'\n[services.node]\nimage='kelvo:1'\n[[services.node.temporary_mounts]]\nmount_path='/var/lib/ts'\nsize_mib=16\n" + extra
}

const goodSidecar = "[[services.node.sidecars]]\nname='tailscale'\nimage='" + sidecarDigest + "'\ncpu='100m'\nmemory='128Mi'\n[services.node.sidecars.env]\nTS_USERSPACE='false'\n[services.node.sidecars.secrets.TS_AUTHKEY]\nref='ts-key'\n[[services.node.sidecars.mounts]]\ntemporary='/var/lib/ts'\nmount_path='/var/lib/tailscale'\n"

func TestSidecarsParseAndNormalize(t *testing.T) {
	app, err := Parse([]byte(sidecarApp(goodSidecar)))
	if err != nil {
		t.Fatal(err)
	}
	sc := app.Services["node"].Sidecars[0]
	if sc.CPU != "100m" || sc.Memory != "128Mi" || sc.Secrets["TS_AUTHKEY"].Ref != "ts-key" {
		t.Fatalf("%+v", sc)
	}
	refs := SecretReferences(app.Services["node"])
	if refs[SidecarSecretKey("tailscale", "TS_AUTHKEY")].Ref != "ts-key" {
		t.Fatal("sidecar secret is not a secret reference")
	}
	if got := PodResources(app.Services["node"]); got.CPURequest != "220m" || got.MemoryLimit != "436Mi" {
		t.Fatalf("pod resources %+v", got)
	}
}

func TestSidecarValidation(t *testing.T) {
	repl := func(old, new string) string { return sidecarApp(strings.Replace(goodSidecar, old, new, 1)) }
	extra := func(name string) string {
		return "[[services.node.sidecars]]\nname='" + name + "'\nimage='" + sidecarDigest + "'\ncpu='10m'\nmemory='16Mi'\n"
	}
	for name, doc := range map[string]string{
		"tag only":          repl("@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", ":latest"),
		"short digest":      repl("@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "@sha256:abc"),
		"bad name":          repl("name='tailscale'", "name='Tail_Scale'"),
		"service name":      repl("name='tailscale'", "name='node'"),
		"reserved":          repl("name='tailscale'", "name='app'"),
		"reserved prefix":   repl("name='tailscale'", "name='hakopod-x'"),
		"duplicate":         sidecarApp(goodSidecar + extra("tailscale")),
		"too many":          sidecarApp(goodSidecar + extra("a") + extra("b") + extra("c") + extra("d")),
		"no cpu":            repl("cpu='100m'\n", ""),
		"no memory":         repl("memory='128Mi'\n", ""),
		"cpu out of bounds": repl("cpu='100m'", "cpu='65'"),
		"bad memory":        repl("memory='128Mi'", "memory='lots'"),
		"secret in env":     repl("TS_USERSPACE='false'", "TS_AUTHKEY='x'"),
		"bad secret ref":    repl("ref='ts-key'", "ref='Bad Ref'"),
		"unknown mount":     repl("temporary='/var/lib/ts'", "temporary='/nope'"),
		"unknown volume":    repl("temporary='/var/lib/ts'", "volume='data'"),
		"both sources":      repl("temporary='/var/lib/ts'", "temporary='/var/lib/ts'\nvolume='data'"),
		"system path":       repl("mount_path='/var/lib/tailscale'", "mount_path='/etc/x'"),
		"unknown key":       repl("cpu='100m'", "cpu='100m'\nprivileged=true"),
		"actions":           "name='kelvo'\n[services.node]\nimage='x:1'\n[services.node.actions]\ncredential='c'\n" + goodSidecar,
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	for name, doc := range map[string]string{
		"serverless": "name='kelvo'\n[services.node]\nimage='x:1'\nport=80\n[services.node.serverless]\n" + goodSidecar,
		"job":        "name='kelvo'\n[services.node]\nimage='x:1'\n[services.node.job]\ntimeout_seconds=60\n" + goodSidecar,
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestSidecarDiffRedactsEnvironment(t *testing.T) {
	app, err := Parse([]byte(sidecarApp(goodSidecar)))
	if err != nil {
		t.Fatal(err)
	}
	changes := Diff(nil, app)
	found := false
	for _, c := range changes {
		if c.Service == "node" && c.Field == "sidecars" {
			found = true
			after := c.After.([]Sidecar)
			if after[0].Env["TS_USERSPACE"] != "[redacted]" || !c.Sensitive {
				t.Fatal("sidecar environment values leaked into the diff")
			}
		}
	}
	if !found {
		t.Fatal("adding a sidecar is not in the plan diff")
	}
	next := app
	next.Services = map[string]Service{"node": app.Services["node"]}
	svc := next.Services["node"]
	svc.Sidecars = nil
	next.Services["node"] = svc
	for _, c := range Diff(&app, next) {
		if c.Field == "sidecars" {
			return
		}
	}
	t.Fatal("removing a sidecar is not in the plan diff")
}

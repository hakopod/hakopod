package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func patchedTarget(t *testing.T, patches spec.KubernetesPatches) (Target, spec.Service) {
	t.Helper()
	target := testTarget(t)
	svc := target.Spec.Services["web"]
	svc.Kubernetes = &patches
	target.Spec.Services["web"] = svc
	return target, svc
}

func TestKubernetesDeploymentPatchApplies(t *testing.T) {
	target, svc := patchedTarget(t, spec.KubernetesPatches{Deployment: `
metadata:
  labels:
    team: shop
    app.kubernetes.io/part-of: storefront
  annotations:
    example.com/owner: shop
spec:
  template:
    metadata:
      annotations:
        prometheus.io/scrape: "true"
    spec:
      tolerations:
        - key: dedicated
          operator: Equal
          value: shop
          effect: NoSchedule
      priorityClassName: high
      volumes:
        - name: scratch
          emptyDir: {}
      containers:
        - name: app
          env:
            - name: GOMAXPROCS
              value: "2"
            - name: POD_NAME
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
          resources:
            limits:
              memory: 512Mi
          volumeMounts:
            - name: scratch
              mountPath: /scratch
          livenessProbe:
            httpGet:
              path: /livez
              port: 8080
`})
	d, err := patchDeployment("web", svc, deployment(target, "web", svc, time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	pod := d.Spec.Template.Spec
	app := pod.Containers[0]
	if len(pod.Tolerations) != 1 || pod.Tolerations[0].Key != "dedicated" || pod.PriorityClassName != "high" {
		t.Fatalf("scheduling patch not applied: %+v", pod.Tolerations)
	}
	env := map[string]corev1.EnvVar{}
	for _, e := range app.Env {
		env[e.Name] = e
	}
	if env["GOMAXPROCS"].Value != "2" || env["POD_NAME"].ValueFrom == nil {
		t.Fatalf("env patch not applied: %+v", app.Env)
	}
	if app.Resources.Limits.Memory().String() != "512Mi" || app.LivenessProbe == nil || len(app.VolumeMounts) != 1 {
		t.Fatalf("container patch not applied: %+v", app)
	}
	if d.Labels["team"] != "shop" || d.Labels["app.kubernetes.io/part-of"] != "storefront" || d.Labels[serviceKey] != "web" || d.Annotations["example.com/owner"] != "shop" {
		t.Fatalf("metadata patch not applied: %+v %+v", d.Labels, d.Annotations)
	}
	if d.Annotations[patchedAnnotationsKey] != "example.com/owner" || d.Spec.Template.Annotations[patchedAnnotationsKey] != "prometheus.io/scrape" {
		t.Fatalf("patched annotation keys not recorded: %v %v", d.Annotations, d.Spec.Template.Annotations)
	}
	if app.Image != svc.Image || *pod.SecurityContext.RunAsNonRoot != true || *pod.AutomountServiceAccountToken {
		t.Fatal("generated security fields changed")
	}
}

func TestKubernetesDeploymentPatchProtectedFields(t *testing.T) {
	pod := func(body string) string {
		return "spec:\n  template:\n    spec:\n" + indent(body, "      ")
	}
	app := func(body string) string {
		return pod("containers:\n  - name: app\n" + indent(body, "    "))
	}
	cases := map[string]struct{ patch, want string }{
		"host-network":        {pod("hostNetwork: true"), "spec.template.spec.hostNetwork is protected"},
		"host-pid":            {pod("hostPID: true"), "spec.template.spec.hostPID is protected"},
		"host-ipc":            {pod("hostIPC: true"), "spec.template.spec.hostIPC is protected"},
		"service-account":     {pod("serviceAccountName: default"), "spec.template.spec.serviceAccountName is protected"},
		"automount-token":     {pod("automountServiceAccountToken: true"), "spec.template.spec.automountServiceAccountToken is protected"},
		"node-name":           {pod("nodeName: control-plane"), "spec.template.spec.nodeName is protected"},
		"runtime-class":       {pod("runtimeClassName: runc"), "spec.template.spec.runtimeClassName is protected"},
		"image-pull-secrets":  {pod("imagePullSecrets:\n  - name: other"), "spec.template.spec.imagePullSecrets is protected"},
		"pod-security":        {pod("securityContext:\n  runAsUser: 0"), "spec.template.spec.securityContext is protected"},
		"system-priority":     {pod("priorityClassName: system-node-critical"), "system priority class"},
		"host-path":           {pod("volumes:\n  - name: root\n    hostPath:\n      path: /"), "only emptyDir and downwardAPI"},
		"secret-volume":       {pod("volumes:\n  - name: s\n    secret:\n      secretName: web-environment"), "only emptyDir and downwardAPI"},
		"projected-token":     {pod("volumes:\n  - name: t\n    projected:\n      sources:\n        - serviceAccountToken:\n            path: token"), "only emptyDir and downwardAPI"},
		"empty-toleration":    {pod("tolerations:\n  - operator: Exists"), "empty key tolerates every taint"},
		"hakopod-toleration":  {pod("tolerations:\n  - key: hakopod.com/pool\n    operator: Exists"), "Hakopod taint"},
		"new-container":       {pod("containers:\n  - name: sidecar\n    image: busybox"), "cannot add or remove containers"},
		"new-init-container":  {pod("initContainers:\n  - name: init\n    image: busybox"), "cannot add or remove containers"},
		"ephemeral":           {pod("ephemeralContainers:\n  - name: debug\n    image: busybox"), "ephemeralContainers is protected"},
		"image":               {app("image: busybox:latest"), `containers["app"].image is protected`},
		"pull-policy":         {app("imagePullPolicy: IfNotPresent"), `containers["app"].imagePullPolicy is protected`},
		"command":             {app("command: [sh]"), `containers["app"].command is protected`},
		"privileged":          {app("securityContext:\n  privileged: true"), `containers["app"].securityContext is protected`},
		"escalation":          {app("securityContext:\n  allowPrivilegeEscalation: true"), `containers["app"].securityContext is protected`},
		"capabilities":        {app("securityContext:\n  capabilities:\n    add: [NET_ADMIN]"), `containers["app"].securityContext is protected`},
		"env-from":            {app("envFrom:\n  - secretRef:\n      name: other"), `containers["app"].envFrom is protected`},
		"env-secret":          {app("env:\n  - name: STOLEN\n    valueFrom:\n      secretKeyRef:\n        name: other-environment\n        key: TOKEN"), "cannot read a Secret or ConfigMap"},
		"gpu":                 {app("resources:\n  limits:\n    nvidia.com/gpu: 1"), "only cpu, memory and ephemeral-storage"},
		"mount-unknown":       {app("volumeMounts:\n  - name: data\n    mountPath: /data"), "may mount only the volumes it adds"},
		"probe-host":          {app("livenessProbe:\n  httpGet:\n    host: 169.254.169.254\n    path: /\n    port: 80"), "probes cannot name a host"},
		"hook-host":           {app("lifecycle:\n  preStop:\n    httpGet:\n      host: 10.0.0.1\n      path: /\n      port: 80"), "lifecycle cannot name a host"},
		"replicas":            {"spec:\n  replicas: 5\n", "spec.replicas is protected"},
		"selector":            {"spec:\n  selector:\n    matchLabels:\n      extra: x\n", "spec.selector is protected"},
		"strategy":            {"spec:\n  strategy:\n    type: Recreate\n", "spec.strategy is protected"},
		"owner-label":         {"metadata:\n  labels:\n    hakopod.io/application-id: other\n", `metadata.labels["hakopod.io/application-id"] is set by Hakopod`},
		"managed-by-label":    {"metadata:\n  labels:\n    app.kubernetes.io/managed-by: helm\n", `metadata.labels["app.kubernetes.io/managed-by"] is set by Hakopod`},
		"revision-annotation": {"metadata:\n  annotations:\n    hakopod.io/revision: \"9\"\n", `is set by Hakopod`},
		"network-label":       {"spec:\n  template:\n    metadata:\n      labels:\n        hakopod.io/net-billing: \"true\"\n", "reserved for Hakopod"},
		"apparmor":            {"spec:\n  template:\n    metadata:\n      annotations:\n        container.apparmor.security.beta.kubernetes.io/app: unconfined\n", "Kubernetes-reserved prefix"},
		"finalizers":          {"metadata:\n  finalizers: [example.com/hold]\n", "metadata.finalizers is protected"},
		"status":              {"status:\n  replicas: 3\n", "status is protected"},
		"typo":                {pod("toleration: []"), "the patched object is invalid"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			target, svc := patchedTarget(t, spec.KubernetesPatches{Deployment: tc.patch})
			_, err := patchDeployment("web", svc, deployment(target, "web", svc, time.Minute))
			var refused *KubernetesPatchError
			if !errors.As(err, &refused) || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "web: kubernetes.deployment patch refused") {
				t.Fatalf("want a refusal naming %q, got %v", tc.want, err)
			}
		})
	}
}

func indent(text, prefix string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n") + "\n"
}

func TestKubernetesDeploymentPatchedAnnotationsLeaveWithThePatch(t *testing.T) {
	ctx := context.Background()
	target, svc := patchedTarget(t, spec.KubernetesPatches{Deployment: "metadata:\n  annotations:\n    example.com/owner: shop\nspec:\n  template:\n    metadata:\n      annotations:\n        prometheus.io/scrape: \"true\"\n"})
	c := &Client{kube: fake.NewClientset(), options: Options{AppDomain: "example.test"}}
	if _, err := c.applyDeployment(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	get := func() (map[string]string, map[string]string) {
		d, err := c.kube.AppsV1().Deployments(Namespace(target.ApplicationID)).Get(ctx, "web", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return d.Annotations, d.Spec.Template.Annotations
	}
	object, pod := get()
	if object["example.com/owner"] != "shop" || pod["prometheus.io/scrape"] != "true" {
		t.Fatalf("patched annotations missing: %v %v", object, pod)
	}
	// A changed value replaces the old one rather than losing to it.
	svc.Kubernetes = &spec.KubernetesPatches{Deployment: "spec:\n  template:\n    metadata:\n      annotations:\n        prometheus.io/scrape: \"false\"\n"}
	target.Spec.Services["web"] = svc
	if _, err := c.applyDeployment(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	object, pod = get()
	if _, ok := object["example.com/owner"]; ok || pod["prometheus.io/scrape"] != "false" {
		t.Fatalf("patch change not reconciled: %v %v", object, pod)
	}
	svc.Kubernetes = nil
	target.Spec.Services["web"] = svc
	if _, err := c.applyDeployment(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	object, pod = get()
	for _, annotations := range []map[string]string{object, pod} {
		for _, key := range []string{"example.com/owner", "prometheus.io/scrape", patchedAnnotationsKey} {
			if _, ok := annotations[key]; ok {
				t.Fatalf("%s stayed after the patch was removed: %v", key, annotations)
			}
		}
	}
}

func TestKubernetesServicePatch(t *testing.T) {
	target, svc := patchedTarget(t, spec.KubernetesPatches{Service: "metadata:\n  annotations:\n    example.com/scrape: \"true\"\nspec:\n  sessionAffinity: ClientIP\n"})
	s, err := patchService("web", svc, serviceObject(target, "web", svc))
	if err != nil {
		t.Fatal(err)
	}
	if s.Spec.SessionAffinity != corev1.ServiceAffinityClientIP || s.Annotations["example.com/scrape"] != "true" || s.Spec.Type != corev1.ServiceTypeClusterIP {
		t.Fatalf("service patch not applied: %+v", s)
	}
	for patch, want := range map[string]string{
		"spec:\n  type: NodePort\n":                                   "spec.type is protected",
		"spec:\n  type: LoadBalancer\n":                               "spec.type is protected",
		"spec:\n  externalIPs: [203.0.113.7]\n":                       "spec.externalIPs is protected",
		"spec:\n  loadBalancerIP: 203.0.113.7\n":                      "spec.loadBalancerIP is protected",
		"spec:\n  clusterIP: None\n":                                  "spec.clusterIP is protected",
		"spec:\n  selector:\n    app: other\n":                        "spec.selector is protected",
		"spec:\n  ports:\n    - port: 8080\n      targetPort: 9090\n": `spec.ports["service"]`,
		"spec:\n  ports:\n    - port: 8080\n      $patch: delete\n":   `spec.ports["service"]`,
		"metadata:\n  labels:\n    hakopod.io/service: other\n":       "is set by Hakopod",
	} {
		svc.Kubernetes = &spec.KubernetesPatches{Service: patch}
		if _, err := patchService("web", svc, serviceObject(target, "web", svc)); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q: want %q, got %v", patch, want, err)
		}
	}
}

func TestKubernetesIngressPatchHAProxyAnnotations(t *testing.T) {
	ctx := context.Background()
	target, svc := patchedTarget(t, spec.KubernetesPatches{Ingress: "metadata:\n  annotations:\n    haproxy.org/timeout-server: 300s\n    haproxy.org/load-balance: leastconn\n"})
	c := &Client{kube: fake.NewClientset(), options: Options{AppDomain: "apps.example.test", IngressClass: "haproxy"}}
	get := func() map[string]string {
		ing, err := c.kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID)).Get(ctx, "web", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return ing.Annotations
	}
	if err := c.applyIngress(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	if a := get(); a["haproxy.org/timeout-server"] != "300s" || a["haproxy.org/load-balance"] != "leastconn" {
		t.Fatalf("haproxy annotations not applied: %v", a)
	}
	// Reapplied on every reconcile, even after something else removed it.
	ing, _ := c.kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID)).Get(ctx, "web", metav1.GetOptions{})
	delete(ing.Annotations, "haproxy.org/load-balance")
	if _, err := c.kube.NetworkingV1().Ingresses(ing.Namespace).Update(ctx, ing, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.applyIngress(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	if a := get(); a["haproxy.org/load-balance"] != "leastconn" {
		t.Fatalf("haproxy annotation not reapplied: %v", a)
	}
	svc.Kubernetes = nil
	if err := c.applyIngress(ctx, target, "web", svc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"haproxy.org/timeout-server", "haproxy.org/load-balance", patchedAnnotationsKey} {
		if value, ok := get()[key]; ok {
			t.Fatalf("%s=%q stayed after the patch was removed", key, value)
		}
	}

	for patch, want := range map[string]string{
		"metadata:\n  annotations:\n    cert-manager.io/cluster-issuer: other\n":                                  "outside haproxy.org/",
		"metadata:\n  annotations:\n    nginx.ingress.kubernetes.io/rewrite-target: /\n":                          "outside haproxy.org/",
		"metadata:\n  annotations:\n    haproxy.org/backend-config-snippet: \"http-request return status 200\"\n": "not available to patches",
		"metadata:\n  annotations:\n    haproxy.org/auth-secret: other/creds\n":                                   "not available to patches",
		"metadata:\n  annotations:\n    haproxy.org/src-ip-header: X-Forwarded-For\n":                             "not available to patches",
		"metadata:\n  annotations:\n    haproxy.org/cr-backend: other/backend\n":                                  "not available to patches",
		"spec:\n  ingressClassName: other\n":                                                                      "spec.ingressClassName is protected",
		"spec:\n  rules:\n    - host: victim.example.com\n":                                                       "spec.rules is protected",
		"spec:\n  tls:\n    - hosts: [victim.example.com]\n      secretName: stolen\n":                            "spec.tls is protected",
	} {
		svc.Kubernetes = &spec.KubernetesPatches{Ingress: patch}
		err := c.applyIngress(ctx, target, "web", svc)
		var refused *KubernetesPatchError
		if !errors.As(err, &refused) || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q: want %q, got %v", patch, want, err)
		}
	}
}

func TestRenderManifestsShowsPatchedObjectsWithoutSecrets(t *testing.T) {
	ctx := context.Background()
	target, svc := patchedTarget(t, spec.KubernetesPatches{
		Deployment: "spec:\n  template:\n    spec:\n      tolerations:\n        - key: dedicated\n          operator: Exists\n",
		Ingress:    "metadata:\n  annotations:\n    haproxy.org/timeout-server: 300s\n",
	})
	svc.Secrets = map[string]spec.SecretRef{"API_TOKEN": {Ref: "api-token"}}
	target.Spec.Services["web"] = svc
	c := &Client{kube: fake.NewClientset(), options: Options{AppDomain: "apps.example.test", IngressClass: "haproxy"}}
	out, err := c.RenderManifests(ctx, target, "web")
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, want := range []string{"kind: Deployment", "kind: Service", "kind: Ingress", "key: dedicated", "haproxy.org/timeout-server: 300s"} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered manifests miss %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "kind: Secret") || strings.Count(text, "\n---\n") != 2 {
		t.Fatalf("rendered manifests must be exactly Deployment, Service and Ingress:\n%s", text)
	}
	actions, _ := c.kube.(*fake.Clientset)
	for _, action := range actions.Actions() {
		if action.GetVerb() != "get" && action.GetVerb() != "list" {
			t.Fatalf("rendering wrote to the cluster: %v", action)
		}
	}
	if _, err := c.RenderManifests(ctx, target, "worker"); err != nil {
		t.Fatalf("a worker without ports renders its Deployment alone: %v", err)
	}

	svc.Kubernetes.Deployment = "spec:\n  template:\n    spec:\n      hostNetwork: true\n"
	target.Spec.Services["web"] = svc
	var refused *KubernetesPatchError
	if err := c.validateKubernetesPatches(ctx, target); !errors.As(err, &refused) {
		t.Fatalf("delivery validation must refuse a protected-field patch before applying: %v", err)
	}
}

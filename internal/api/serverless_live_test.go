package api

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"

	"github.com/hakopod/hakopod/internal/worker"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func TestLiveServerlessIngressNodePlacementAndRecovery(t *testing.T) {
	if os.Getenv("HAKOPOD_SERVERLESS_TEST") != "1" {
		t.Skip("requires named development cluster and private gateway listener")
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("refusing non-development cluster")
	}
	address := os.Getenv("HAKOPOD_TEST_GATEWAY_ADDRESS")
	if address == "" {
		t.Fatal("missing development gateway address")
	}
	host, _, _ := net.SplitHostPort(address)
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	db := notificationTestDB(t)
	kube, err := cluster.New(path, cluster.Options{ServerlessAddress: address, AppDomain: "127.0.0.1.sslip.io", IngressClass: "haproxy", ProxyNamespace: "haproxy-controller", ProxyConfigMap: "hakopod-ingress-kubernetes-ingress", ProxyRelease: "hakopod-ingress", RolloutTimeout: 90 * time.Second, ApprovedDomains: db.ApprovedDomains})
	if err != nil {
		t.Fatal(err)
	}
	restConfig, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatal(err)
	}
	k, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		t.Fatal(err)
	}
	token, err := db.Bootstrap(ctx, "serverless-development-fixture")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	originalProxy, err := k.CoreV1().ConfigMaps("haproxy-controller").Get(ctx, "hakopod-ingress-kubernetes-ingress", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		current, e := k.CoreV1().ConfigMaps(originalProxy.Namespace).Get(cleanup, originalProxy.Name, metav1.GetOptions{})
		if e != nil {
			t.Error(e)
			return
		}
		for _, key := range []string{"log-format", "syslog-server", "logasap"} {
			if value, ok := originalProxy.Data[key]; ok {
				current.Data[key] = value
			} else {
				delete(current.Data, key)
			}
		}
		_, e = k.CoreV1().ConfigMaps(current.Namespace).Update(cleanup, current, metav1.UpdateOptions{})
		if e != nil {
			t.Error(e)
		}
	})
	server := &Server{Store: db, Cluster: kube}
	gateway := server.ServerlessGateway()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: gateway, ReadHeaderTimeout: 5 * time.Second}
	go httpServer.Serve(listener)
	t.Cleanup(func() { httpServer.Close() })
	db.ValidateDeployment = func(ctx context.Context, a store.Application, next spec.Application) error {
		return kube.ValidateDelivery(ctx, cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Revision: a.Revision, Spec: next})
	}
	code := `from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
class Handler(BaseHTTPRequestHandler):
 def do_POST(self):
  body=self.rfile.read(int(self.headers.get('Content-Length','0')))
  print('FUNCTION_INVOKED '+body.decode(),flush=True)
  payload=json.dumps({'body':body.decode(),'pod':os.environ['HOSTNAME']}).encode()
  self.send_response(200)
  self.send_header('Content-Type','application/json')
  self.send_header('Content-Length',str(len(payload)))
  self.end_headers()
  self.wfile.write(payload)
ThreadingHTTPServer(('0.0.0.0',8080),Handler).serve_forever()
`
	makeService := func(node string, warm int32) spec.Service {
		return spec.Service{Image: "python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a", NodeName: node, Public: true, Port: 8080, Serverless: &spec.Serverless{MinReplicas: warm, IdleSeconds: 30, StartupTimeoutSeconds: 60, RequestTimeoutSeconds: 10, MaxConcurrency: 4}, Command: []string{"python", "-u", "/app/function.py"}, Files: map[string]spec.File{"function": {MountPath: "/app/function.py", Content: &code}}}
	}
	app, err := spec.Normalize(spec.Application{Name: "serverless-live", Services: map[string]spec.Service{"api": makeService("k3d-shared-free-test-0", 0), "warm": makeService("k3d-hakopod-dev-server-0", 1)}})
	if err != nil {
		t.Fatal(err)
	}
	javascript := "import { createServer } from 'node:http'; createServer((req,res) => {res.writeHead(200);res.end('javascript-function');}).listen(8080,'0.0.0.0');"
	js := app.Services["warm"]
	js.Image = "node:24-alpine"
	js.Command = []string{"node", "/app/function.mjs"}
	js.Files = map[string]spec.File{"function": {MountPath: "/app/function.mjs", Content: &javascript}}
	app.Services["warm"] = js
	dep, err := db.Accept(ctx, principal, "demo", "development", app, 0, "serverless-live-initial")
	if err != nil {
		t.Fatal(err)
	}
	namespace := cluster.Namespace(dep.ApplicationID)
	t.Log("development fixture namespace", namespace)
	t.Cleanup(func() {
		if t.Failed() && os.Getenv("HAKOPOD_KEEP_FAILED_SERVERLESS_TEST") == "1" {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		ns, e := k.CoreV1().Namespaces().Get(cleanup, namespace, metav1.GetOptions{})
		if e == nil && ns.Labels["app.kubernetes.io/managed-by"] == "hakopod" {
			k.CoreV1().Namespaces().Delete(cleanup, namespace, metav1.DeleteOptions{})
		}
	})
	running, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		(&worker.Worker{Store: db, Cluster: kube, Concurrency: 1, Timeout: 3 * time.Minute}).Run(running)
	}()
	defer func() { stopWorker(); <-workerDone }()
	wait := func(label string, fn func() bool) {
		t.Helper()
		deadline := time.Now().Add(150 * time.Second)
		for time.Now().Before(deadline) && ctx.Err() == nil {
			if fn() {
				return
			}
			time.Sleep(time.Second)
		}
		t.Fatal("timed out: " + label)
	}
	wait("immutable release", func() bool {
		a, e := db.Application(ctx, dep.ApplicationID)
		if e != nil {
			t.Fatal(e)
		}
		if a.Status == "failed" {
			d, _ := db.Deployment(ctx, dep.ID)
			t.Fatal(d.Error)
		}
		return a.Status == "healthy"
	})
	pods, err := k.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, pod := range pods.Items {
		svc := pod.Labels["hakopod.io/service"]
		if wanted, ok := app.Services[svc]; ok && pod.Spec.NodeName != wanted.NodeName {
			t.Fatalf("%s scheduled on %s instead of %s", svc, pod.Spec.NodeName, wanted.NodeName)
		}
	}
	if err = gateway.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	t.Log("immutable release deployed; both services are pinned to the requested nodes")
	server.collectRequests(ctx)
	var gatewayWG sync.WaitGroup
	gatewayWG.Add(1)
	go func() { defer gatewayWG.Done(); gateway.Run(running) }()
	defer func() { stopWorker(); gatewayWG.Wait() }()
	wait("idle scale to zero", func() bool {
		d, e := k.AppsV1().Deployments(namespace).Get(ctx, "api", metav1.GetOptions{})
		return e == nil && d.Spec.Replicas != nil && *d.Spec.Replicas == 0 && d.Status.Replicas == 0
	})
	warm, err := k.AppsV1().Deployments(namespace).Get(ctx, "warm", metav1.GetOptions{})
	if err != nil || *warm.Spec.Replicas != 1 {
		t.Fatal("always warm scaled down", err)
	}
	target := cluster.Target{ApplicationID: dep.ApplicationID, Revision: 1, Project: "demo", Environment: "development", Spec: app}
	publicHost := kube.ServiceHostname(target, "api")
	jsRequest, _ := http.NewRequestWithContext(ctx, "GET", "http://"+net.JoinHostPort(host, "30080")+"/", nil)
	jsRequest.Host = kube.ServiceHostname(target, "warm")
	jsClient := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: nil}}
	jsResponse, e := jsClient.Do(jsRequest)
	if e != nil {
		t.Fatal(e)
	}
	jsBody, e := io.ReadAll(jsResponse.Body)
	jsResponse.Body.Close()
	if e != nil || jsResponse.StatusCode != 200 || string(jsBody) != "javascript-function" {
		t.Fatal("JavaScript function did not serve HTTP", jsResponse.StatusCode, string(jsBody), e)
	}

	client := &http.Client{Timeout: 80 * time.Second, Transport: &http.Transport{Proxy: nil}}
	request := func(value string) (int, string, error) {
		r, e := http.NewRequestWithContext(ctx, "POST", "http://"+net.JoinHostPort(host, "30080")+"/invoke?not-recorded=yes", strings.NewReader(value))
		if e != nil {
			return 0, "", e
		}
		r.Host = publicHost
		res, e := client.Do(r)
		if e != nil {
			return 0, "", e
		}
		defer res.Body.Close()
		data, e := io.ReadAll(res.Body)
		return res.StatusCode, string(data), e
	}
	var requests sync.WaitGroup
	for i := 0; i < 3; i++ {
		requests.Add(1)
		go func(i int) {
			defer requests.Done()
			status, body, e := request(fmt.Sprintf("request-%d", i))
			if e != nil || status != 200 || !strings.Contains(body, fmt.Sprintf("request-%d", i)) {
				t.Errorf("cold request %d: %d %s %v", i, status, body, e)
			}
		}(i)
	}
	requests.Wait()
	if t.Failed() {
		return
	}
	logs, err := kube.Logs(ctx, namespace, "api", 100, false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(logs)
	logs.Close()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "FUNCTION_INVOKED") != 3 {
		t.Fatal("request replay or missing invocation", string(data))
	}
	routing, err := kube.RequestRouting(ctx, target, "api")
	if err != nil || len(routing.Routes) != 1 || len(routing.Endpoints) == 0 {
		t.Fatal("routing visualization missing activation route", routing, err)
	}
	bindings, truncated, err := server.requestBindings(ctx)
	if err != nil || truncated || bindings[namespace+"_svc_"+cluster.ActivationServiceName("api")+"_http"].Service != "api" {
		t.Fatal("request collector binding missing", err)
	}
	wait("captured serverless requests", func() bool {
		server.collectRequests(ctx)
		r, e := db.Requests(ctx, principal, store.RequestQuery{ApplicationID: dep.ApplicationID, Service: "api"})
		if e != nil {
			t.Fatal(e)
		}
		return len(r.Items) >= 3
	})
	t.Log("three simultaneous POST requests woke one container and each reached it exactly once; routing and recorded Requests verified")
	// A manual stop is a new immutable revision and must never be undone by traffic.
	svc := app.Services["api"]
	svc.Suspended = true
	app.Services["api"] = svc
	dep2, err := db.Accept(ctx, principal, "demo", "development", app, 1, "serverless-live-stop")
	if err != nil {
		t.Fatal(err)
	}
	wait("stop revision", func() bool {
		d, e := db.Deployment(ctx, dep2.ID)
		if e != nil {
			t.Fatal(e)
		}
		if d.Status == "failed" {
			t.Fatal(d.Error)
		}
		return d.Status == "succeeded"
	})
	status, _, err := request("must-not-run")
	if err != nil {
		t.Fatal(err)
	}
	if status != 503 && status != 404 {
		t.Fatal("stopped service accepted traffic", status)
	}
	d, err := k.AppsV1().Deployments(namespace).Get(ctx, "api", metav1.GetOptions{})
	if err != nil || *d.Spec.Replicas != 0 {
		t.Fatal("manual stop was undone")
	}
	t.Log("manual stop remains stopped after public traffic")
}

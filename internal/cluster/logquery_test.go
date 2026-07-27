package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/logquery"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestLogSearchMetadataBudgetAndBufferedCancellation(t *testing.T) {
	target := testTarget(t)
	now := time.Now().UTC().Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/log"):
			w.Header().Set("Content-Type", "text/plain")
			if r.URL.Query().Get("previous") == "true" {
				fmt.Fprintf(w, "%s %s0%s\n", now.Format(time.RFC3339Nano), strings.Repeat(`{"nested":`, 1000), strings.Repeat(`}`, 1000))
				return
			}
			for i := 0; i < 1000; i++ {
				line := map[string]any{"sequence": i, "level": "error"}
				for field := 0; field < 20; field++ {
					line[fmt.Sprintf("k%02d", field)] = map[string]any{"v": i}
				}
				body, _ := json.Marshal(line)
				fmt.Fprintf(w, "%s %s\n", now.Add(time.Duration(i-1000)*time.Millisecond).Format(time.RFC3339Nano), body)
			}
		case strings.HasSuffix(r.URL.Path, "/pods"):
			_ = json.NewEncoder(w).Encode(corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, Items: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "web-budget", Namespace: Namespace(target.ApplicationID), Labels: labelsFor(target, "web")}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}}}})
		default:
			_ = json.NewEncoder(w).Encode(corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labelsFor(target, "")}})
		}
	}))
	defer server.Close()
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{kube: kube}
	o := LogQueryOptions{Service: "web", Limit: 1000}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	result, err := c.QueryLogs(context.Background(), target, o, func(logquery.Entry) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	budget := 0
	for _, entry := range result.Entries {
		budget += entry.RetainedBytes()
	}
	if budget > 2<<20 || len(result.Entries) >= 1000 || len(result.Entries) == 0 || !result.Truncated || result.Matched != 1000 {
		t.Fatalf("structured maps bypassed the retained memory budget: bytes=%d returned=%d matched=%d", budget, len(result.Entries), result.Matched)
	}
	if result.Entries[len(result.Entries)-1].Fields["sequence"] != json.Number("999") {
		t.Fatal("memory truncation did not preserve the newest matching entry")
	}
	o.Previous = true
	omitted, err := c.QueryLogs(context.Background(), target, o, func(entry logquery.Entry) bool { return strings.Contains(entry.Message, "nested") })
	if err != nil || len(omitted.Entries) != 1 || len(omitted.Entries[0].Fields) != 0 || len(omitted.Warnings) != 2 || !strings.Contains(omitted.Warnings[1], "Structured fields were omitted") {
		t.Fatalf("metadata omission lost the raw match or its warning: %v", err)
	}
	o.Previous = false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	_, err = c.QueryLogs(ctx, target, o, func(logquery.Entry) bool {
		calls++
		cancel()
		return true
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("buffered filtering continued after cancellation: predicates=%d error=%v", calls, err)
	}
}

func TestLogSearchSamplesOwnedPodsAndKeepsNewestMatches(t *testing.T) {
	target := testTarget(t)
	var requests atomic.Int32
	now := time.Now().UTC().Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/log"):
			requests.Add(1)
			if r.URL.Query().Get("limitBytes") != "1048576" || r.URL.Query().Get("tailLines") != "2000" || r.URL.Query().Get("sinceSeconds") != "3600" || r.URL.Query().Get("timestamps") != "true" {
				t.Error("unbounded Kubernetes log read", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "text/plain")
			for i := 0; i < 20; i++ {
				fmt.Fprintf(w, "%s {\"level\":\"error\",\"sequence\":%d}\n", now.Add(time.Duration(i-20)*time.Second).Format(time.RFC3339Nano), i)
			}
		case strings.HasSuffix(r.URL.Path, "/pods"):
			if !strings.Contains(r.URL.Query().Get("labelSelector"), ownerID(target.ApplicationID)) {
				t.Error("missing ownership filter")
			}
			list := corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}}
			for i := 0; i < 10; i++ {
				list.Items = append(list.Items, corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("web-%d", i), Namespace: Namespace(target.ApplicationID), Labels: labelsFor(target, "web"), CreationTimestamp: metav1.NewTime(now.Add(time.Duration(i) * time.Second))}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}})
			}
			json.NewEncoder(w).Encode(list)
		default:
			json.NewEncoder(w).Encode(corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labelsFor(target, "")}})
		}
	}))
	defer server.Close()
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{kube: kube}
	o := LogQueryOptions{Service: "web", Query: "json.sequence >= 10", Limit: 5}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	match, _ := logquery.Compile(o.Query)
	result, err := c.QueryLogs(context.Background(), target, o, match)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 8 || result.Pods != 8 || result.Scanned != 160 || result.Matched != 80 || !result.Truncated || len(result.Entries) != 5 {
		t.Fatalf("incorrect bounded sample: requests=%d result=%+v", requests.Load(), result)
	}
	for _, e := range result.Entries {
		if !strings.Contains(e.Message, `"sequence":19`) {
			t.Fatal("did not retain newest entries", e.Message)
		}
	}
	count := 0
	for _, bucket := range result.Histogram {
		count += bucket.Count
	}
	if count != 80 {
		t.Fatal("histogram does not describe matched sample", count)
	}
}

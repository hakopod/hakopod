package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/applogs"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
)

// RunAppLogs keeps application logs after their pods are gone. It does
// nothing when the log directory is unavailable, as on development machines.
func (s *Server) RunAppLogs(ctx context.Context) {
	if s.Store == nil || s.Cluster == nil {
		return
	}
	logs := &applogs.Store{Index: s.Store}
	if err := logs.Available(); err != nil {
		slog.Info("Application log history is off on this machine: " + err.Error())
		return
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	c := &applogs.Collector{
		Cluster: &appLogCluster{kube: s.Cluster, db: s.Store},
		Records: appLogRecords{s.Store},
		Store:   logs,
		Owner:   hex.EncodeToString(id),
	}
	c.Run(ctx)
}

// appLogRecords adds the one method whose store type applogs cannot name.
type appLogRecords struct{ *store.Store }

func (r appLogRecords) Settings(ctx context.Context) (applogs.Settings, error) {
	v, _, err := r.Store.AppLogSettings(ctx)
	return applogs.Settings{Enabled: v.Enabled, RetentionDays: v.RetentionDays, MaxMiBPerApp: v.MaxMiBPerApp}, err
}

// appLogCluster adapts the cluster client. Pod labels carry a hash of the
// application ID, so it maps namespaces back to real IDs and drops pods that
// belong to no known application.
type appLogCluster struct {
	kube *cluster.Client
	db   *store.Store

	mu   sync.Mutex
	at   time.Time
	byNS map[string]string
}

func (a *appLogCluster) apps(ctx context.Context) map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.byNS != nil && time.Since(a.at) < time.Minute {
		return a.byNS
	}
	rows, err := a.db.Pool.Query(ctx, "SELECT id FROM applications LIMIT 20000")
	if err != nil {
		return a.byNS
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			m[cluster.Namespace(id)] = id
		}
	}
	if rows.Err() == nil {
		a.byNS, a.at = m, time.Now()
	}
	return a.byNS
}

func (a *appLogCluster) AppLogPods(ctx context.Context, limit int) ([]applogs.Pod, error) {
	pods, err := a.kube.AppLogPods(ctx, limit)
	if err != nil {
		return nil, err
	}
	ids := a.apps(ctx)
	out := make([]applogs.Pod, 0, len(pods))
	for _, p := range pods {
		id := ids[p.Namespace]
		if id == "" {
			continue
		}
		q := applogs.Pod{ApplicationID: id, Service: p.Service, Namespace: p.Namespace, Name: p.Name, UID: p.UID, Node: p.Node, Reason: p.Reason, Revision: p.Revision, StartedAt: p.StartedAt, Terminating: p.Terminating}
		for _, k := range p.Containers {
			q.Containers = append(q.Containers, applogs.Container(k))
		}
		out = append(out, q)
	}
	return out, nil
}

func (a *appLogCluster) PodLogSince(ctx context.Context, namespace, pod, container string, previous bool, since time.Time, limitBytes int64) ([]byte, error) {
	return a.kube.PodLogSince(ctx, namespace, pod, container, previous, since, limitBytes)
}

func (a *appLogCluster) AppLogMasker(ctx context.Context, namespace, pod string) (applogs.Masker, error) {
	return a.kube.AppLogMasker(ctx, namespace, pod)
}

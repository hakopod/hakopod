package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
)

func (s *Server) databaseConnectionPlan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ApplicationID string `json:"application_id"`
		Service       string `json:"service"`
		Variable      string `json:"variable"`
		Endpoint      string `json:"endpoint"`
		ClusterAware  bool   `json:"cluster_aware"`
	}
	if !decode(w, r, &in) {
		return
	}
	plan, err := s.Store.PlanDatabaseConnection(r.Context(), who(r), r.PathValue("id"), in.ApplicationID, in.Service, in.Variable, in.Endpoint, in.ClusterAware)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, plan)
}
func (s *Server) databaseConnect(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ReviewID           string `json:"review_id"`
		ConfirmApplication string `json:"confirm_application"`
	}
	if !decode(w, r, &in) {
		return
	}
	idem, ok := backupIdempotency(w, r)
	if !ok {
		return
	}
	deployment, err := s.Store.AcceptDatabaseConnection(r.Context(), who(r), r.PathValue("id"), in.ReviewID, in.ConfirmApplication, idem)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 202, deployment)
}
func (s *Server) databaseInspect(w http.ResponseWriter, r *http.Request) {
	var in struct {
		JobID            string `json:"job_id"`
		ConfirmName      string `json:"confirm_name"`
		ExpectedRevision int64  `json:"expected_revision"`
		Inspected        bool   `json:"inspected"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !in.Inspected {
		problem(w, 400, "invalid_request", "Confirm that you inspected the recovered data.")
		return
	}
	d, err := s.Store.InspectDatabaseRecovery(r.Context(), who(r), r.PathValue("id"), in.JobID, in.ConfirmName, in.ExpectedRevision)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, d)
}

func (s *Server) ConfigureDatabaseBindings() {
	if s.Cluster == nil || s.Store == nil {
		return
	}
	s.Cluster.SetDatabaseBindingResolver(func(ctx context.Context, project, environment string, app spec.Application) (map[string]map[string]cluster.DatabaseConnection, error) {
		databases, err := s.Store.ResolveDatabaseBindings(ctx, project, environment, app)
		if err != nil {
			return nil, err
		}
		passwords := map[string][]byte{}
		trusts := map[string]string{}
		for id, d := range databases {
			observed, err := s.Cluster.ObserveDatabase(ctx, d)
			if err != nil || observed.Status != "ready" {
				return nil, fmt.Errorf("managed database is not healthy")
			}
			d.Observation = observed
			databases[id] = d
			if d.Spec.TLSRequired() {
				trust, err := s.Cluster.DatabaseTrust(ctx, d)
				if err != nil {
					return nil, err
				}
				trusts[id] = trust.CertificatePEM
			}
			passwords[id], err = database.OpenCredentials(s.authEncryptionKey(), d)
			if err != nil {
				return nil, err
			}
		}
		result, err := s.resolveExternalDatabaseConnections(ctx, project, environment, app)
		if err != nil {
			return nil, err
		}
		for name, svc := range app.Services {
			if result[name] == nil {
				result[name] = map[string]cluster.DatabaseConnection{}
			}
			for variable, b := range svc.Bindings {
				if b.ManagedDatabase == "" {
					continue
				}
				d := databases[b.ManagedDatabase]
				found := false
				for _, endpoint := range d.Observation.Endpoints {
					if endpoint.Purpose != b.Endpoint {
						continue
					}
					user, db := "app", "app"
					if d.Spec.Engine == "vitess" {
						db = "app@primary"
						if b.Endpoint == "read_only" {
							db = "app@replica"
						}
					}
					if d.Spec.Engine == "oracle" {
						user, db = "APP", "FREEPDB1"
						if d.Spec.Oracle != nil && d.Spec.Oracle.Edition == "enterprise" {
							db = "APPDB"
						}
					}
					if d.Spec.Engine == "redis" {
						user, db = "default", "0"
					}
					u := url.URL{Scheme: b.Protocol, Host: net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port)), Path: "/" + db, User: url.UserPassword(user, string(passwords[d.ID]))}
					if d.Spec.TLSRequired() {
						if d.Spec.Engine == "postgresql" {
							query := u.Query()
							query.Set("sslmode", "verify-full")
							query.Set("sslrootcert", cluster.DatabaseTrustPath(d.ID))
							u.RawQuery = query.Encode()
						} else if d.Spec.Engine == "redis" {
							u.Scheme = "rediss"
						} else if d.Spec.Engine == "clickhouse" {
							query := u.Query()
							query.Set("secure", "true")
							query.Set("skip_verify", "false")
							u.RawQuery = query.Encode()
						} else if d.Spec.Engine == "oracle" {
							query := u.Query()
							query.Set("SSL", "enable")
							query.Set("SSL VERIFY", "true")
							query.Set("FAST LOGIN", "false")
							u.RawQuery = query.Encode()
						} else if d.Spec.Engine == "mongodb" {
							query := u.Query()
							for key, value := range map[string]string{"tls": "true", "tlsCAFile": cluster.DatabaseTrustPath(d.ID), "replicaSet": "database", "authSource": "app", "w": "majority", "readConcernLevel": "majority", "readPreference": "primary", "retryWrites": "true"} {
								query.Set(key, value)
							}
							u.RawQuery = query.Encode()
						}
					}
					result[name][variable] = cluster.DatabaseConnection{URL: u.String(), Port: int32(endpoint.Port), CA: trusts[d.ID]}
					found = true
					break
				}
				if !found {
					return nil, fmt.Errorf("managed database endpoint is unavailable")
				}
			}
		}
		return result, nil
	})
}

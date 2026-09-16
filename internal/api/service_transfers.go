package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func (s *Server) registerServiceTransferRoutes(routes *http.ServeMux) {
	routes.HandleFunc("POST /api/v1/applications/{id}/services/{service}/move-plan", s.planServiceTransfer)
	routes.HandleFunc("POST /api/v1/applications/{id}/services/{service}/move", s.startServiceTransfer)
	routes.HandleFunc("GET /api/v1/applications/{id}/service-moves", s.serviceTransfers)
	routes.HandleFunc("POST /api/v1/applications/{id}/service-moves/{move}/finish", s.finishServiceTransfer)
}

type transferInput struct {
	DestinationID       string `json:"destination_id"`
	DestinationService  string `json:"destination_service"`
	SourceRevision      int64  `json:"source_revision"`
	DestinationRevision int64  `json:"destination_revision"`
}

func (s *Server) transferPlan(w http.ResponseWriter, r *http.Request) (store.Application, store.Application, spec.Application, spec.Application, store.ServiceTransfer, bool) {
	var in transferInput
	var source, destination store.Application
	var before, after spec.Application
	var move store.ServiceTransfer
	if !decode(w, r, &in) {
		return source, destination, before, after, move, false
	}
	source, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return source, destination, before, after, move, false
	}
	destination, ok = s.authorizedApp(w, r, in.DestinationID, "deployments:write")
	if !ok {
		return source, destination, before, after, move, false
	}
	if source.ID == destination.ID || source.Project != destination.Project || source.Environment != destination.Environment {
		problem(w, 400, "invalid_destination", "Choose another application in the same project and environment.")
		return source, destination, before, after, move, false
	}
	if strings.HasSuffix(r.URL.Path, "/move") {
		var priorID string
		if err := s.Store.Pool.QueryRow(r.Context(), "SELECT id FROM deployments WHERE identity_id=$1 AND idempotency_key=$2", who(r).ID, r.Header.Get("Idempotency-Key")).Scan(&priorID); err == nil {
			prior, err := s.Store.ServiceTransfer(r.Context(), priorID)
			if err != nil || prior.SourceID != source.ID || prior.DestinationID != destination.ID || prior.Service != r.PathValue("service") || prior.DestinationService != in.DestinationService || prior.SourceRevision != in.SourceRevision || prior.DestinationRevision != in.DestinationRevision {
				problem(w, 409, "idempotency_conflict", "This request key was already used for a different operation.")
				return source, destination, before, after, move, false
			}
			d, err := s.Store.Deployment(r.Context(), priorID)
			if err != nil {
				failure(w, err)
			} else {
				write(w, 202, d)
			}
			return source, destination, before, after, move, false
		}
	}
	if source.Revision != in.SourceRevision || destination.Revision != in.DestinationRevision {
		problem(w, 409, "stale_revision", "An application changed. Reload both applications and review the move again.")
		return source, destination, before, after, move, false
	}
	move = store.ServiceTransfer{SourceID: source.ID, DestinationID: destination.ID, SourceRevision: source.Revision, DestinationRevision: destination.Revision, Service: r.PathValue("service"), DestinationService: in.DestinationService}
	if err := s.Store.CheckServiceTransfer(r.Context(), who(r), source.Project, source.Environment, move); err != nil {
		transferFailure(w, err)
		return source, destination, before, after, move, false
	}
	// Move exactly the running images, including the untouched destination services.
	src, err := s.pinnedApplication(r.Context(), source)
	if err != nil {
		transferFailure(w, err)
		return source, destination, before, after, move, false
	}
	dst, err := s.pinnedApplication(r.Context(), destination)
	if err != nil {
		transferFailure(w, err)
		return source, destination, before, after, move, false
	}
	before, after, err = spec.MoveService(src, dst, move.Service, move.DestinationService)
	if err != nil {
		transferFailure(w, err)
		return source, destination, before, after, move, false
	}
	svc := after.Services[move.DestinationService]
	remapMoveSecrets(&svc, move)
	after.Services[move.DestinationService] = svc
	if !s.validateDeliveryPlan(w, r, source.Project, source.Environment, before, &source) || !s.validateDeliveryPlan(w, r, destination.Project, destination.Environment, after, &destination) {
		return source, destination, before, after, move, false
	}
	return source, destination, before, after, move, true
}

func transferFailure(w http.ResponseWriter, err error) {
	problem(w, 409, "move_unavailable", err.Error())
}

func (s *Server) pinnedApplication(ctx context.Context, app store.Application) (spec.Application, error) {
	next, err := spec.Normalize(app.Spec)
	if err != nil {
		return next, err
	}
	var resolved *spec.Application
	err = s.Store.Pool.QueryRow(ctx, "SELECT resolved_spec FROM deployments WHERE application_id=$1 AND revision=$2 AND status='succeeded'", app.ID, app.Revision).Scan(&resolved)
	if err != nil || resolved == nil {
		return next, fmt.Errorf("wait for the current application deployment to succeed before changing its services")
	}
	for name, svc := range next.Services {
		current, ok := resolved.Services[name]
		if !ok {
			return next, fmt.Errorf("the current image for %s is unavailable", name)
		}
		svc.Image = current.Image
		svc.RegistryCredential = current.RegistryCredential
		next.Services[name] = svc
	}
	return next, nil
}

func moveSecretName(move store.ServiceTransfer, ref string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s:%d:%s:%s:%s", move.SourceID, move.SourceRevision, move.DestinationID, move.DestinationRevision, move.Service, move.DestinationService, ref)))
	return "move-" + hex.EncodeToString(sum[:16])
}

func remapMoveSecrets(svc *spec.Service, move store.ServiceTransfer) {
	for key, ref := range svc.Secrets {
		if ref.Ref != "" {
			ref.Ref = moveSecretName(move, ref.Ref)
			svc.Secrets[key] = ref
		}
	}
	for key, file := range svc.Files {
		if file.Secret != nil && file.Secret.Ref != "" {
			ref := *file.Secret
			ref.Ref = moveSecretName(move, ref.Ref)
			file.Secret = &ref
			svc.Files[key] = file
		}
	}
}

func (s *Server) planServiceTransfer(w http.ResponseWriter, r *http.Request) {
	source, destination, before, after, move, ok := s.transferPlan(w, r)
	if !ok {
		return
	}
	refs := spec.SecretReferences(spec.EffectiveService(source.Spec, source.Spec.Services[move.Service]))
	write(w, 200, map[string]any{"source": before, "destination": after, "source_changes": spec.Diff(&source.Spec, before), "destination_changes": spec.Diff(&destination.Spec, after), "secret_count": len(refs), "warnings": []string{
		"The original keeps running until you finish the move. Both copies may process work during this period.",
		"The new service gets a new default URL and private address. Update clients and any addresses in variables or files before finishing.",
		"Existing environment values are retained as service overrides; additional destination application defaults also apply. Local secrets are copied without replacing destination secrets.",
		"Git build configuration and deployment history stay with the original application. Update source configuration before enabling automatic deployments again.",
	}})
}

func (s *Server) startServiceTransfer(w http.ResponseWriter, r *http.Request) {
	source, destination, _, after, move, ok := s.transferPlan(w, r)
	if !ok {
		return
	}
	refs := map[string]bool{}
	for _, ref := range spec.SecretReferences(spec.EffectiveService(source.Spec, source.Spec.Services[move.Service])) {
		refs[ref.Ref] = true
	}
	if len(refs) > 0 {
		if s.Cluster == nil {
			problem(w, 503, "secret_unavailable", "Secret storage is unavailable.")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		names := []string{}
		for name := range refs {
			names = append(names, name)
		}
		sort.Strings(names)
		values, err := s.Cluster.ReadWorkloadSecrets(ctx, source.Project, source.Environment, source.Name, names)
		if err != nil {
			problem(w, 409, "secret_unavailable", "A source secret is unavailable. Check the application's Secrets page.")
			return
		}
		for _, name := range names {
			target := moveSecretName(move, name)
			err = s.Cluster.CreateWorkloadSecret(ctx, destination.Project, destination.Environment, destination.Name, target, values[name])
			if apierrors.IsAlreadyExists(err) {
				saved, e := s.Cluster.ReadWorkloadSecrets(ctx, destination.Project, destination.Environment, destination.Name, []string{target})
				if e == nil && saved[target] == values[name] {
					err = nil
				}
			}
			if err != nil {
				problem(w, 409, "secret_copy_failed", "A destination secret could not be copied safely. The original service is unchanged.")
				return
			}
		}
	}
	d, err := s.Store.AcceptServiceTransfer(r.Context(), who(r), source, destination, after, move, r.Header.Get("Idempotency-Key"))
	if err != nil {
		transferFailure(w, err)
		return
	}
	write(w, 202, d)
}

func (s *Server) serviceTransfers(w http.ResponseWriter, r *http.Request) {
	source, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	items, err := s.Store.ServiceTransfers(r.Context(), source.ID)
	if err != nil {
		failure(w, err)
		return
	}
	visible := []store.ServiceTransfer{}
	for _, item := range items {
		destination, err := s.Store.Application(r.Context(), item.DestinationID)
		if err == nil && who(r).Allows("deployments:write", destination.Project, destination.Environment, destination.Name) {
			visible = append(visible, item)
		}
	}
	write(w, 200, map[string]any{"items": visible})
}

func (s *Server) finishServiceTransfer(w http.ResponseWriter, r *http.Request) {
	source, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	move, err := s.Store.ServiceTransfer(r.Context(), r.PathValue("move"))
	if err != nil {
		failure(w, err)
		return
	}
	if move.SourceID != source.ID {
		failure(w, store.ErrForbidden)
		return
	}
	destination, ok := s.authorizedApp(w, r, move.DestinationID, "deployments:write")
	if !ok {
		return
	}
	if move.RemovalID != "" {
		d, err := s.Store.Deployment(r.Context(), move.RemovalID)
		if err != nil {
			failure(w, err)
			return
		}
		write(w, 202, d)
		return
	}
	if err = s.Store.CheckServiceTransfer(r.Context(), who(r), source.Project, source.Environment, move); err != nil {
		transferFailure(w, err)
		return
	}
	next, err := s.pinnedApplication(r.Context(), source)
	if err != nil {
		transferFailure(w, err)
		return
	}
	delete(next.Services, move.Service)
	next, err = spec.Normalize(next)
	if err != nil {
		transferFailure(w, err)
		return
	}
	if !s.validateDeliveryPlan(w, r, source.Project, source.Environment, next, &source) {
		return
	}
	d, err := s.Store.AcceptServiceTransfer(r.Context(), who(r), source, destination, next, move, r.Header.Get("Idempotency-Key"))
	if err != nil {
		transferFailure(w, err)
		return
	}
	write(w, 202, d)
}

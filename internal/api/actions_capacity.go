package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/hakopod/hakopod/internal/actions/vm"
	"github.com/hakopod/hakopod/internal/store"
)

// The installation ID is the hakopod-installation tag value the documented
// IAM policy conditions TerminateInstances on.
type actionsCapacityView struct {
	store.ActionsCapacitySettings
	InstallationID string `json:"installation_id"`
	Revision       int64  `json:"revision"`
}

func (s *Server) writeActionsCapacity(w http.ResponseWriter, r *http.Request, v store.ActionsCapacitySettings, rev int64) {
	id, err := s.Store.InstallationID(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, actionsCapacityView{v, id, rev})
}

func (s *Server) getActionsCapacity(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	v, rev, err := s.Store.ActionsCapacitySettings(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	s.writeActionsCapacity(w, r, v, rev)
}

func (s *Server) putActionsCapacity(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	var in struct {
		Capacities       []store.ActionsCapacity `json:"capacities"`
		ExpectedRevision int64                   `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Capacities == nil {
		in.Capacities = []store.ActionsCapacity{}
	}
	v, rev, err := s.Store.PutActionsCapacity(r.Context(), who(r), in.Capacities, in.ExpectedRevision)
	if err != nil {
		failure(w, err)
		return
	}
	s.writeActionsCapacity(w, r, v, rev)
}

// testActionsCapacity proves a capacity's role and network settings. With
// launch it also starts the smallest on-demand instance of its architecture,
// which powers off at boot, and terminates it straight away.
func (s *Server) testActionsCapacity(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	var in struct {
		Launch bool `json:"launch"`
	}
	if !decode(w, r, &in) {
		return
	}
	c, ok, err := s.Store.ActionsCapacity(r.Context(), r.PathValue("name"))
	if err != nil {
		failure(w, err)
		return
	}
	if !ok {
		problem(w, 404, "not_found", "No Actions capacity has this name.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	out := map[string]any{"capacity": c.Name, "external_id": c.ExternalID, "warnings": []string{}}
	refuse := func(err error) {
		var capacity *actionsCapacityError
		message := "The capacity check did not complete."
		if errors.As(err, &capacity) {
			message = capacity.Error()
		} else if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			message = err.Error() // vm/aws errors carry only AWS error codes
		}
		problem(w, 502, "capacity_unavailable", message)
	}
	p, err := s.actionsVMProvider(ctx, c)
	if err != nil {
		refuse(err)
		return
	}
	warnings, err := p.Check(ctx)
	if err != nil {
		refuse(err)
		return
	}
	out["warnings"] = warnings
	if in.Launch {
		tiny := map[string]string{"amd64": "t3.micro", "arm64": "t4g.micro"}[c.Architecture]
		i, err := p.Launch(ctx, vm.LaunchInput{Name: "test-" + store.NewID()[:12], InstanceTypes: []string{tiny}, UserData: "#!/bin/bash\nshutdown -h now\n",
			Tags: map[string]string{vm.TagCapacity: c.Name, vm.TagSlot: "test"}})
		if err != nil {
			refuse(err)
			return
		}
		out["instance_id"], out["instance_type"] = i.ID, i.Type
		if err = p.Terminate(ctx, i.ID); err != nil {
			// The instance powers itself off; the reaper also removes it.
			out["warnings"] = append(warnings, "The test instance was launched but not yet terminated: "+err.Error())
		}
		_, _ = s.Store.Pool.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'settings.actions_capacity.test','installation',$3)", who(r).ID, who(r).KeyID, store.JSON(map[string]string{"capacity": c.Name, "instance_id": i.ID}))
	}
	write(w, 200, out)
}

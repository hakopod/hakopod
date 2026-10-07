package operations

import (
	"context"
	"errors"
)

// Access describes one fixed connection. Installation and project scope cannot mix.
type Access struct {
	Installation     bool
	AllowWrite       bool
	AllowDeploy      bool
	AllowAdmin       bool
	AllowCredentials bool
}

func InvokeWithAccess(ctx context.Context, r RequestFunc, scope Scope, access Access, in Invocation) (any, error) {
	if !access.Installation {
		if access.AllowAdmin {
			return nil, errors.New("administration requires a separate installation connection")
		}
		credentialRequired := false
		for _, op := range catalog {
			if op.ID == in.Operation {
				credentialRequired = op.Policy.CredentialRequired
			}
		}
		if credentialRequired {
			if !access.AllowCredentials {
				return nil, errors.New("credential operations require explicit credential opt-in")
			}
			if err := requireProjectCredentialGrant(ctx, r, scope); err != nil {
				return nil, err
			}
		}
		return invoke(ctx, r, scope, access.AllowWrite, in, false, access.AllowCredentials, access.AllowDeploy)
	}
	if scope.Project != "" || scope.Environment != "" {
		return nil, errors.New("installation connections cannot include project or environment")
	}
	if !access.AllowAdmin {
		return nil, errors.New("installation operations require explicit administration opt-in")
	}
	if !InstallationAvailable(in.Operation) {
		return nil, errors.New("this operation is not available on an installation connection")
	}
	ownerRequired := false
	credentialRequired := false
	for _, op := range catalog {
		if op.ID == in.Operation {
			ownerRequired = op.Policy.ScopeRequirement == "installation_owner"
			credentialRequired = op.Policy.CredentialRequired
		}
	}
	if credentialRequired && !access.AllowCredentials {
		return nil, errors.New("credential operations require explicit credential opt-in")
	}
	if err := requireInstallation(ctx, r, access.AllowCredentials, ownerRequired); err != nil {
		return nil, err
	}
	return invoke(ctx, r, scope, access.AllowWrite, in, true, access.AllowCredentials, access.AllowDeploy)
}
func RequireInstallation(ctx context.Context, r RequestFunc, credentials bool) error {
	return requireInstallation(ctx, r, credentials, false)
}
func requireInstallation(ctx context.Context, r RequestFunc, credentials, owner bool) error {
	var p struct {
		Owner          bool     `json:"owner"`
		Permissions    []string `json:"permissions"`
		Project        string   `json:"project"`
		Environment    string   `json:"environment"`
		Application    string   `json:"application"`
		CredentialType string   `json:"credential_type"`
	}
	if err := r(ctx, "GET", "/me", nil, "", &p); err != nil {
		return err
	}
	grants := map[string]bool{}
	for _, permission := range p.Permissions {
		grants[permission] = true
	}
	if p.Project != "" || p.Environment != "" || p.Application != "" || !grants["admin"] || !grants["agent:admin"] || (p.CredentialType != "machine" && p.CredentialType != "cli") {
		return errors.New("installation access requires explicit admin and agent:admin grants on an installation credential")
	}
	if owner && !p.Owner {
		return errors.New("installation maintenance requires the current installation owner")
	}
	if credentials && !grants["agent:credentials"] {
		return errors.New("credential access requires an explicit agent:credentials grant")
	}
	return nil
}

func hasSensitiveFields(v any, fields []string) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			for _, field := range fields {
				if k == field {
					return true
				}
			}
			if hasSensitiveFields(val, fields) {
				return true
			}
		}
	case []any:
		for _, val := range x {
			if hasSensitiveFields(val, fields) {
				return true
			}
		}
	}
	return false
}

func InstallationAvailable(id string) bool {
	for _, op := range catalog {
		if op.ID == id {
			return op.Policy.Exposure == "installation"
		}
	}
	return false
}

func requireProjectCredential(ctx context.Context, r RequestFunc, scope Scope) error {
	var p struct {
		Project        string `json:"project"`
		Environment    string `json:"environment"`
		Application    string `json:"application"`
		CredentialType string `json:"credential_type"`
	}
	if err := r(ctx, "GET", "/me", nil, "", &p); err != nil {
		return err
	}
	if p.Project != scope.Project || p.Environment != scope.Environment || p.Application != "" || (p.CredentialType != "machine" && p.CredentialType != "cli") {
		return errors.New("this operation requires a credential for the exact project and environment, without an application restriction")
	}
	return nil
}

func requireProjectCredentialGrant(ctx context.Context, r RequestFunc, scope Scope) error {
	var p struct {
		Project     string   `json:"project"`
		Environment string   `json:"environment"`
		Application string   `json:"application"`
		Permissions []string `json:"permissions"`
	}
	if err := r(ctx, "GET", "/me", nil, "", &p); err != nil {
		return err
	}
	granted := false
	for _, permission := range p.Permissions {
		if permission == "agent:credentials" {
			granted = true
		}
	}
	if !granted || p.Project != scope.Project || p.Environment != scope.Environment || p.Application != "" {
		return errors.New("credential access requires agent:credentials for the exact project and environment")
	}
	return nil
}

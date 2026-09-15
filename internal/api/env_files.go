package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

// Imported secrets use keyed, application-scoped names. Unchanged imports are
// retryable; changed values get new references and therefore roll out normally.
func (s *Server) importEnvironment(ctx context.Context, p store.Principal, project, environment, expectedName string, data []byte, files map[string]string) (spec.Application, error) {
	paths, err := spec.EnvironmentFileNames(data)
	if err != nil {
		return spec.Application{}, err
	}
	if len(paths) == 0 && len(files) == 0 {
		return spec.Parse(data)
	}
	result, err := spec.ImportEnvironmentFiles(data, files, func(string, string) string { return "envfile-" + store.NewID() })
	if err != nil {
		return spec.Application{}, err
	}
	return s.saveImportedEnvironment(ctx, p, project, environment, expectedName, result.Spec, result.Secrets)
}

func (s *Server) saveImportedEnvironment(ctx context.Context, p store.Principal, project, environment, expectedName string, app spec.Application, values map[string]string) (spec.Application, error) {
	name := app.Name
	if !validScope(project, environment) || !p.Allows("deployments:write", project, environment, name) || expectedName != "" && name != expectedName {
		return spec.Application{}, store.ErrForbidden
	}
	if len(values) == 0 {
		return app, nil
	}
	if len(s.authEncryptionKey()) != 32 || s.Cluster == nil {
		return spec.Application{}, fmt.Errorf("%w: configure authentication encryption and Kubernetes secret storage before importing sensitive environment files", store.ErrInput)
	}
	secrets := map[string]string{}
	replacements := map[string]string{}
	for old, value := range values {
		mac := hmac.New(sha256.New, s.authEncryptionKey())
		mac.Write(store.JSON([]string{"hakopod-env-file-v1", project, environment, name, value}))
		ref := "envfile-" + hex.EncodeToString(mac.Sum(nil))[:32]
		replacements[old], secrets[ref] = ref, value
	}
	replace := func(refs map[string]spec.SecretRef) {
		for key, ref := range refs {
			if next, ok := replacements[ref.Ref]; ok {
				ref.Ref = next
				refs[key] = ref
			}
		}
	}
	replace(app.Secrets)
	for _, service := range app.Services {
		replace(service.Secrets)
	}
	var err error
	app, err = spec.Normalize(app)
	if err != nil {
		return spec.Application{}, err
	}
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, reference := range names {
		if err := s.Cluster.PutWorkloadSecret(ctx, project, environment, name, reference, secrets[reference]); err != nil {
			return spec.Application{}, fmt.Errorf("env_file: secret upload failed; keep the files attached and retry")
		}
		if _, err := s.Store.Pool.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'secret.write',$3)", p.ID, p.KeyID, project+"/"+environment+"/"+name+"/"+reference); err != nil {
			return spec.Application{}, fmt.Errorf("env_file: could not record secret upload; retry")
		}
	}
	return app, nil
}

// Source imports resolve all files at the same reviewed commit and directory.
type environmentImportScope struct {
	Principal   store.Principal
	Project     string
	Environment string
	Application string
}

func (s *Server) importSourceEnvironment(ctx context.Context, b sourceBinding, commit string, data []byte, scope environmentImportScope) (spec.Application, error) {
	names, err := spec.EnvironmentFileNames(data)
	if err != nil {
		return spec.Application{}, err
	}
	files := map[string]string{}
	total := 0
	for _, name := range names {
		body, err := s.buildDetectionFile(ctx, buildConfig{Provider: b.Provider, ConnectionID: b.ConnectionID, Repository: b.Repository}, commit, path.Join(path.Dir(b.Path), name))
		if err != nil {
			return spec.Application{}, fmt.Errorf("env_file: could not read %s at the reviewed commit; upload ignored/local files through the configuration editor", name)
		}
		total += len(body)
		if total > spec.MaxEnvironmentFileBytes {
			return spec.Application{}, fmt.Errorf("env_file: combined files exceed 128 KiB")
		}
		files[name] = string(body)
	}
	return s.importEnvironment(ctx, scope.Principal, scope.Project, scope.Environment, scope.Application, data, files)
}

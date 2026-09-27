package main

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/pelletier/go-toml/v2"
)

// mergeFile is one TOML document found in an application folder.
type mergeFile struct {
	Path string // path on disk, used verbatim in error messages
	Data []byte // the file's bytes
}

// singleOwnerKeys are the application-level keys at most one file may set.
var singleOwnerKeys = []string{"name", "recovery", "inject_env", "domains", "volumes"}

const envFileUnsupported = "env_file is not supported when several files are merged; put those services in one hakopod.toml, or use secret references"

// strictDecode decodes one file into target, rejecting unknown keys so that a
// typo like imagee is reported instead of silently dropped. It reports field
// locations the way spec.Parse does, never the decoder's contextual source
// excerpt, which would echo the file's values back.
func strictDecode(path string, data []byte, target any) error {
	err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(target)
	var strict *toml.StrictMissingError
	if errors.As(err, &strict) {
		fields := make([]string, 0, len(strict.Errors))
		for i, item := range strict.Errors {
			if i >= 16 {
				break
			}
			line, column := item.Position()
			fields = append(fields, fmt.Sprintf("%s (line %d, column %d)", strings.Join(item.Key(), "."), line, column))
		}
		return fmt.Errorf("%s: unknown TOML fields: %s", path, strings.Join(fields, ", "))
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// trimmed re-encodes a document whose legitimate-but-fieldless keys have been
// removed, so the strict decoder never sees them. Callers pass the original
// bytes back when nothing was removed, which keeps error positions exact.
func trimmed(document map[string]any, data []byte, removed bool) ([]byte, error) {
	if !removed {
		return data, nil
	}
	return toml.Marshal(document)
}

// dropEnvFile removes env_file at both levels. Only the exempted lone
// hakopod.toml reaches it; every other file is rejected before decoding.
func dropEnvFile(document map[string]any) bool {
	removed := false
	if _, carries := document["env_file"]; carries {
		delete(document, "env_file")
		removed = true
	}
	services, _ := document["services"].(map[string]any)
	for _, value := range services {
		table, isTable := value.(map[string]any)
		if !isTable {
			continue
		}
		if _, carries := table["env_file"]; carries {
			delete(table, "env_file")
			removed = true
		}
	}
	return removed
}

// mergeFolder merges every file into one application named appName. Files must
// already be sorted by Path so that errors are deterministic. It does no disk
// access; the returned application is not normalized.
func mergeFolder(appName string, files []mergeFile) (spec.Application, error) {
	if len(files) == 0 {
		return spec.Application{}, errors.New("no TOML files to merge")
	}
	legacy := len(files) == 1 && filepath.Base(files[0].Path) == "hakopod.toml"

	merged := spec.Application{Name: appName}
	// owner records which file first set a key, for "both paths" conflict errors.
	owner := map[string]string{}
	claim := func(key, path string) error {
		if first, taken := owner[key]; taken {
			return fmt.Errorf("%s and %s both set %s", first, path, key)
		}
		owner[key] = path
		return nil
	}
	// agreeSchemaVersion applies the agree-or-conflict rule to one file's
	// schema_version. Both document shapes route through it: a bare service file
	// carries the key just as often as an application document does.
	agreeSchemaVersion := func(version int, path string) error {
		if version == 0 {
			return nil
		}
		if merged.SchemaVersion != 0 && merged.SchemaVersion != version {
			return fmt.Errorf("%s and %s set different schema_version values (%d and %d)", owner["schema_version"], path, merged.SchemaVersion, version)
		}
		merged.SchemaVersion = version
		if _, seen := owner["schema_version"]; !seen {
			owner["schema_version"] = path
		}
		return nil
	}

	for _, file := range files {
		if !legacy {
			if names, err := spec.EnvironmentFileNames(file.Data); err == nil && len(names) > 0 {
				return spec.Application{}, fmt.Errorf("%s: %s", file.Path, envFileUnsupported)
			}
		}

		var document map[string]any
		if err := toml.Unmarshal(file.Data, &document); err != nil {
			return spec.Application{}, fmt.Errorf("%s: %w", file.Path, err)
		}

		removed := legacy && dropEnvFile(document)

		if _, isApplication := document["services"]; !isApplication {
			// A bare service file: the service is named after the file. Its
			// schema_version is an application-level key, not a service field, so
			// take it out of the table before the rest becomes the service.
			if value, carries := document["schema_version"]; carries {
				delete(document, "schema_version")
				removed = true
				version, ok := value.(int64)
				if !ok {
					return spec.Application{}, fmt.Errorf("%s: schema_version must be an integer", file.Path)
				}
				if err := agreeSchemaVersion(int(version), file.Path); err != nil {
					return spec.Application{}, err
				}
			}
			data, err := trimmed(document, file.Data, removed)
			if err != nil {
				return spec.Application{}, fmt.Errorf("%s: %w", file.Path, err)
			}
			var service spec.Service
			if err := strictDecode(file.Path, data, &service); err != nil {
				return spec.Application{}, err
			}
			name := strings.TrimSuffix(filepath.Base(file.Path), ".toml")
			if err := claim("services."+name, file.Path); err != nil {
				return spec.Application{}, fmt.Errorf("%s and %s both define service %q", owner["services."+name], file.Path, name)
			}
			if merged.Services == nil {
				merged.Services = map[string]spec.Service{}
			}
			merged.Services[name] = service
			continue
		}

		data, err := trimmed(document, file.Data, removed)
		if err != nil {
			return spec.Application{}, fmt.Errorf("%s: %w", file.Path, err)
		}
		var app spec.Application
		if err := strictDecode(file.Path, data, &app); err != nil {
			return spec.Application{}, err
		}

		for _, key := range singleOwnerKeys {
			if _, set := document[key]; !set {
				continue
			}
			if err := claim(key, file.Path); err != nil {
				return spec.Application{}, err
			}
		}
		if app.Name != "" && app.Name != appName {
			return spec.Application{}, fmt.Errorf("%s sets name %q but the folder is application %q", file.Path, app.Name, appName)
		}
		if err := agreeSchemaVersion(app.SchemaVersion, file.Path); err != nil {
			return spec.Application{}, err
		}
		if app.Recovery != nil {
			merged.Recovery = app.Recovery
		}
		if app.InjectEnv {
			merged.InjectEnv = true
		}
		for key, value := range app.Domains {
			if merged.Domains == nil {
				merged.Domains = map[string]string{}
			}
			merged.Domains[key] = value
		}
		for key, value := range app.Volumes {
			if merged.Volumes == nil {
				merged.Volumes = map[string]spec.NamedVolume{}
			}
			merged.Volumes[key] = value
		}

		for key, value := range app.Networks {
			if err := claim("networks."+key, file.Path); err != nil {
				return spec.Application{}, fmt.Errorf("%s and %s both define networks.%s", owner["networks."+key], file.Path, key)
			}
			if merged.Networks == nil {
				merged.Networks = map[string]spec.Network{}
			}
			merged.Networks[key] = value
		}
		for key, value := range app.Env {
			if err := claim("env."+key, file.Path); err != nil {
				return spec.Application{}, fmt.Errorf("%s and %s both define env.%s", owner["env."+key], file.Path, key)
			}
			if merged.Env == nil {
				merged.Env = map[string]string{}
			}
			merged.Env[key] = value
		}
		for key, value := range app.Secrets {
			if err := claim("secrets."+key, file.Path); err != nil {
				return spec.Application{}, fmt.Errorf("%s and %s both define secrets.%s", owner["secrets."+key], file.Path, key)
			}
			if merged.Secrets == nil {
				merged.Secrets = map[string]spec.SecretRef{}
			}
			merged.Secrets[key] = value
		}
		for name, service := range app.Services {
			if err := claim("services."+name, file.Path); err != nil {
				return spec.Application{}, fmt.Errorf("%s and %s both define service %q", owner["services."+name], file.Path, name)
			}
			if merged.Services == nil {
				merged.Services = map[string]spec.Service{}
			}
			merged.Services[name] = service
		}
	}
	merged.Name = appName
	return merged, nil
}

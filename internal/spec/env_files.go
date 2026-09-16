package spec

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
)

const MaxEnvironmentFileBytes = 128 << 10

// Environment files are import inputs, never paths read by the runtime host.
// The caller supplies their contents and opaque references for sensitive values.
type EnvironmentImport struct {
	Spec    Application
	Secrets map[string]string
}

func EnvironmentFilePaths(value any) ([]string, error) {
	var names []string
	switch v := value.(type) {
	case string:
		names = []string{v}
	case []any:
		for _, item := range v {
			name, ok := item.(string)
			if !ok {
				return nil, errors.New("env_file: use a relative filename or an array of filenames")
			}
			names = append(names, name)
		}
	case nil:
		return nil, nil
	default:
		return nil, errors.New("env_file: use a relative filename or an array of filenames")
	}
	if len(names) == 0 || len(names) > 8 {
		return nil, errors.New("env_file: provide 1–8 relative filenames")
	}
	seen := map[string]bool{}
	for _, name := range names {
		if name == "" || len(name) > 200 || path.IsAbs(name) || path.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\:\x00\r\n") || seen[name] {
			return nil, errors.New("env_file: use unique paths inside the configuration directory, without parent traversal")
		}
		seen[name] = true
	}
	return names, nil
}

func environmentDocument(data []byte) (map[string]any, map[string][]string, error) {
	if len(data) > MaxBytes {
		return nil, nil, errors.New("application TOML exceeds 256 KiB")
	}
	var document map[string]any
	if err := toml.Unmarshal(data, &document); err != nil {
		return nil, nil, errors.New("invalid TOML syntax or field type")
	}
	refs := map[string][]string{}
	read := func(table map[string]any, service string) error {
		value, exists := table["env_file"]
		if !exists {
			return nil
		}
		names, err := EnvironmentFilePaths(value)
		if err != nil {
			return err
		}
		refs[service] = names
		delete(table, "env_file")
		return nil
	}
	if err := read(document, ""); err != nil {
		return nil, nil, err
	}
	if services, ok := document["services"].(map[string]any); ok {
		for name, value := range services {
			if table, ok := value.(map[string]any); ok {
				if err := read(table, name); err != nil {
					return nil, nil, err
				}
			}
		}
	}
	return document, refs, nil
}

func EnvironmentFileNames(data []byte) ([]string, error) {
	_, refs, err := environmentDocument(data)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, names := range refs {
		for _, name := range names {
			seen[name] = true
		}
	}
	if len(seen) > 8 {
		return nil, errors.New("env_file: at most 8 files per application import")
	}
	var result []string
	for name := range seen {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

// ParseDotenv parses assignments as literal data. It never expands host values
// or executes shell syntax. Errors contain line numbers, never source values.
func ParseDotenv(data string) (map[string]string, error) {
	if len(data) > MaxEnvironmentFileBytes || !utf8.ValidString(data) || strings.ContainsRune(data, 0) {
		return nil, errors.New("env_file: use UTF-8 text without NUL, at most 128 KiB")
	}
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(strings.TrimPrefix(data, "\ufeff"), "\r\n", "\n"), "\r", "\n"), "\n")
	values := map[string]string{}
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") || strings.HasPrefix(line, "export\t") {
			line = strings.TrimSpace(line[6:])
		}
		key, value, ok := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || !envPattern.MatchString(key) || len(key) > 128 {
			return nil, fmt.Errorf("env_file: invalid assignment on line %d; use NAME=value", i+1)
		}
		if _, exists := values[key]; exists {
			return nil, fmt.Errorf("env_file: duplicate variable on line %d", i+1)
		}
		if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
			quote := value[0]
			source, end := value[1:], -1
			pos := 0
			for end < 0 {
				for pos < len(source) {
					if quote == '"' && source[pos] == '\\' {
						pos += 2
						continue
					}
					if source[pos] == quote {
						end = pos
						break
					}
					pos++
				}
				if end >= 0 {
					break
				}
				i++
				if i >= len(lines) {
					return nil, errors.New("env_file: unclosed quoted value")
				}
				source += "\n" + lines[i]
			}

			if tail := strings.TrimSpace(source[end+1:]); tail != "" && !strings.HasPrefix(tail, "#") {
				return nil, errors.New("env_file: unexpected text after a quoted value")
			}
			value = source[:end]
			if quote == '"' {
				value = strings.NewReplacer(`\n`, "\n", `\r`, "\r", `\t`, "\t", `\"`, `"`, `\\`, `\`).Replace(value)
			}
		} else {
			// A # without preceding whitespace is part of an unquoted value.
			for j := 0; j < len(value); j++ {
				if value[j] == '#' && (j == 0 || value[j-1] == ' ' || value[j-1] == '\t') {
					value = value[:j]
					break
				}
			}
			value = strings.TrimRight(value, " \t")
		}
		if len(value) > 64<<10 || (len(value) > 4096 && !sensitiveEnv(key, value)) {
			return nil, errors.New("env_file: plain values must be at most 4096 bytes and secrets at most 64 KiB")
		}
		values[key] = value
		if len(values) > 128 {
			return nil, errors.New("env_file: at most 128 variables per file")
		}
	}
	return values, nil
}

func ImportEnvironmentFiles(data []byte, files map[string]string, bind func(string, string) string) (EnvironmentImport, error) {
	document, refs, err := environmentDocument(data)
	if err != nil {
		return EnvironmentImport{}, err
	}
	names, err := EnvironmentFileNames(data)
	if err != nil {
		return EnvironmentImport{}, err
	}
	if len(files) > 8 {
		return EnvironmentImport{}, errors.New("env_file: at most 8 files")
	}
	parsed, total := map[string]map[string]string{}, 0
	for _, name := range names {
		body, ok := files[name]
		if !ok {
			return EnvironmentImport{}, fmt.Errorf("env_file: upload %s or place it beside your configuration for the CLI", name)
		}
		total += len(body)
		if total > MaxEnvironmentFileBytes {
			return EnvironmentImport{}, errors.New("env_file: combined files exceed 128 KiB")
		}
		parsed[name], err = ParseDotenv(body)
		if err != nil {
			return EnvironmentImport{}, err
		}
	}
	for name := range files {
		if _, ok := parsed[name]; !ok {
			return EnvironmentImport{}, errors.New("env_file: an uploaded file is not referenced by the configuration")
		}
	}
	secrets := map[string]string{}
	for service, names := range refs {
		table := document
		if service != "" {
			table = document["services"].(map[string]any)[service].(map[string]any)
		} else {
			if value, exists := table["inject_env"]; exists && value != true {
				return EnvironmentImport{}, errors.New("env_file: application files enable shared injection; remove inject_env = false or remove env_file")
			}
			table["inject_env"] = true
		}
		env, refs := map[string]any{}, map[string]any{}
		if existing, exists := table["env"]; exists {
			var ok bool
			env, ok = existing.(map[string]any)
			if !ok {
				return EnvironmentImport{}, errors.New("env: expected a table")
			}
		}
		if existing, exists := table["secrets"]; exists {
			var ok bool
			refs, ok = existing.(map[string]any)
			if !ok {
				return EnvironmentImport{}, errors.New("secrets: expected a table")
			}
		}
		merged := map[string]string{}
		for _, name := range names {
			for key, value := range parsed[name] {
				merged[key] = value
			}
		}
		for key, value := range merged {
			if _, ok := env[key]; ok {
				continue
			}
			if _, ok := refs[key]; ok {
				continue
			}
			if sensitiveEnv(key, value) {
				if value == "" {
					return EnvironmentImport{}, errors.New("env_file: sensitive variables need a nonempty value or an explicit secret reference")
				}
				if bind == nil {
					return EnvironmentImport{}, errors.New("env_file: secret storage is required for sensitive values")
				}
				ref := bind(key, value)
				refs[key] = map[string]any{"ref": ref}
				secrets[ref] = value
			} else {
				env[key] = value
			}
		}
		table["env"], table["secrets"] = env, refs
	}
	encoded, err := toml.Marshal(document)
	if err != nil {
		return EnvironmentImport{}, errors.New("env_file: could not encode imported configuration")
	}
	app, err := Parse(encoded)
	if err != nil {
		return EnvironmentImport{}, err
	}
	return EnvironmentImport{Spec: app, Secrets: secrets}, nil
}

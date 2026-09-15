package spec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/pelletier/go-toml/v2"
	"go.yaml.in/yaml/v3"
)

// ComposeImport is a draft, not an accepted deployment. No Docker daemon,
// process environment, filesystem, or network is consulted during conversion.
type ComposeImport struct {
	Spec     Application       `json:"spec"`
	TOML     string            `json:"toml"`
	Warnings []string          `json:"warnings"`
	Secrets  map[string]string `json:"-"`
}

type composeReader struct {
	variables map[string]string
	warnings  []string
	files     map[string]string
	envRefs   map[string]any
	bind      func(string, string) string
	secrets   map[string]string
}

// ImportCompose converts a bounded image-based Compose document. base is copied
// and only new services may be added; shared definitions must match exactly.
func ImportCompose(data []byte, name string, variables map[string]string, base *Application) (ComposeImport, error) {
	return ImportComposeWithEnvironmentFiles(data, name, variables, base, nil, nil)
}

func ImportComposeWithEnvironmentFiles(data []byte, name string, variables map[string]string, base *Application, files map[string]string, bind func(string, string) string) (ComposeImport, error) {
	total := 0
	for _, body := range files {
		total += len(body)
	}
	if len(files) > 8 || total > MaxEnvironmentFileBytes {
		return ComposeImport{}, errors.New("env_file: use at most 8 files totalling 128 KiB")
	}
	if len(data) == 0 || len(data) > MaxBytes {
		return ComposeImport{}, errors.New("compose: provide a YAML document of at most 256 KiB")
	}
	if len(variables) > 128 {
		return ComposeImport{}, errors.New("variables: at most 128 values")
	}
	for k, v := range variables {
		if !envPattern.MatchString(k) || len(k) > 128 || len(v) > 4096 {
			return ComposeImport{}, errors.New("variables: use environment names and values of at most 4096 bytes")
		}
	}
	d := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := d.Decode(&doc); err != nil {
		return ComposeImport{}, errors.New("compose: invalid YAML syntax")
	}
	var extra yaml.Node
	if d.Decode(&extra) != io.EOF {
		return ComposeImport{}, errors.New("compose: provide exactly one YAML document")
	}
	count := 0
	if err := composeTree(&doc, 0, &count); err != nil {
		return ComposeImport{}, err
	}
	if len(doc.Content) != 1 {
		return ComposeImport{}, errors.New("compose: expected a document")
	}
	r := &composeReader{variables: variables, warnings: []string{}, files: files, envRefs: map[string]any{}, bind: bind, secrets: map[string]string{}}
	root, err := composeMap(doc.Content[0], "compose", "name", "version", "services", "networks", "volumes")
	if err != nil {
		return ComposeImport{}, err
	}
	if name == "" {
		name, err = r.text(root["name"], "name")
		if err != nil {
			return ComposeImport{}, err
		}
	}
	app := Application{SchemaVersion: 1, Name: name, Services: map[string]Service{}, Networks: map[string]Network{}, Volumes: map[string]NamedVolume{}}
	if base != nil {
		app, err = Normalize(*base)
		if err != nil {
			return ComposeImport{}, err
		}
		if name != "" && name != app.Name {
			return ComposeImport{}, errors.New("name: keep the existing application name")
		}
	}
	if !namePattern.MatchString(app.Name) {
		return ComposeImport{}, errors.New("name: provide an application name using 1–40 lowercase letters, digits or hyphens")
	}
	if root["version"] != nil {
		r.warn("Compose version is ignored; the generated configuration uses Hakopod schema version 1.")
	}
	networks, err := composeMap(root["networks"], "networks")
	if err != nil {
		return ComposeImport{}, err
	}
	for _, n := range composeKeys(networks) {
		if !namePattern.MatchString(n) {
			return ComposeImport{}, fmt.Errorf("networks.%s: use lowercase letters, digits or hyphens; update service references too", n)
		}
		fields, e := composeMap(networks[n], "networks."+n, "internal", "driver")
		if e != nil {
			return ComposeImport{}, e
		}
		var net Network
		if fields["internal"] != nil {
			if e = fields["internal"].Decode(&net.Internal); e != nil {
				return ComposeImport{}, fmt.Errorf("networks.%s.internal: expected a boolean", n)
			}
		}
		if fields["driver"] != nil {
			driver, e := r.text(fields["driver"], "networks."+n+".driver")
			if e != nil {
				return ComposeImport{}, e
			}
			if driver != "bridge" {
				return ComposeImport{}, fmt.Errorf("networks.%s.driver: only application-local bridge networks can be converted", n)
			}
		}
		if old, ok := app.Networks[n]; ok && old != net {
			return ComposeImport{}, fmt.Errorf("networks.%s: conflicts with the existing application network", n)
		}
		app.Networks[n] = net
	}
	if app.Volumes == nil {
		app.Volumes = map[string]NamedVolume{}
	}
	volumes, err := composeMap(root["volumes"], "volumes")
	if err != nil {
		return ComposeImport{}, err
	}
	for _, n := range composeKeys(volumes) {
		fields, e := composeMap(volumes[n], "volumes."+n, "x-hakopod")
		if e != nil {
			return ComposeImport{}, e
		}
		v := NamedVolume{SizeGiB: 1, AccessMode: "ReadWriteOnce"}
		if fields["x-hakopod"] != nil {
			if e = composeJSON(fields["x-hakopod"], &v); e != nil {
				return ComposeImport{}, fmt.Errorf("volumes.%s.x-hakopod: use size_gib, storage_class and access_mode", n)
			}
		} else {
			r.warn(fmt.Sprintf("Volume %s becomes a new 1 GiB persistent volume. Existing Docker volume data is not copied; adjust size_gib before deployment.", n))
		}
		if old, ok := app.Volumes[n]; ok && old != v {
			return ComposeImport{}, fmt.Errorf("volumes.%s: conflicts with the existing application volume", n)
		}
		app.Volumes[n] = v
	}
	services, err := composeMap(root["services"], "services")
	if err != nil {
		return ComposeImport{}, err
	}
	if len(services) == 0 || len(services)+len(app.Services) > 20 {
		return ComposeImport{}, errors.New("services: provide 1–20 services, including existing services")
	}
	for _, n := range composeKeys(services) {
		if _, ok := app.Services[n]; ok {
			return ComposeImport{}, fmt.Errorf("services.%s: a service with this name already exists; rename the imported service and its references", n)
		}
		svc, e := r.service(n, services[n])
		if e != nil {
			return ComposeImport{}, e
		}
		app.Services[n] = svc
	}
	if len(r.envRefs) > 0 || len(files) > 0 {
		// Fill omitted defaults before the strict TOML round-trip, including
		// default network membership for services without a networks declaration.
		app, err = Normalize(app)
		if err != nil {
			return ComposeImport{}, err
		}
		raw, e := toml.Marshal(app)
		if e != nil {
			return ComposeImport{}, e
		}
		var doc map[string]any
		if e = toml.Unmarshal(raw, &doc); e != nil {
			return ComposeImport{}, e
		}
		for name, value := range r.envRefs {
			doc["services"].(map[string]any)[name].(map[string]any)["env_file"] = value
		}
		raw, e = toml.Marshal(doc)
		if e != nil {
			return ComposeImport{}, e
		}
		imported, e := ImportEnvironmentFiles(raw, files, bind)
		if e != nil {
			return ComposeImport{}, e
		}
		app, r.secrets = imported.Spec, imported.Secrets
	}

	app, err = Normalize(app)
	if err != nil {
		return ComposeImport{}, err
	}
	encoded, err := toml.Marshal(app)
	if err != nil {
		return ComposeImport{}, errors.New("compose: could not generate TOML")
	}
	// Round-trip through the canonical strict parser before presenting a draft.
	if _, err = Parse(encoded); err != nil {
		return ComposeImport{}, err
	}
	r.warn("Review resource sizes and container permissions before deploying. Services use Hakopod resource profiles and its non-root security defaults.")
	return ComposeImport{Spec: app, TOML: string(encoded), Warnings: r.warnings, Secrets: r.secrets}, nil
}

func composeTree(n *yaml.Node, depth int, count *int) error {
	*count = *count + 1
	if depth > 32 || *count > 16384 {
		return errors.New("compose: YAML nesting or node limit exceeded")
	}
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return errors.New("compose: expand YAML anchors and aliases before importing")
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || seen[k.Value] {
				return fmt.Errorf("compose: duplicate or non-string mapping key at line %d", k.Line)
			}
			seen[k.Value] = true
		}
	}
	for _, child := range n.Content {
		if err := composeTree(child, depth+1, count); err != nil {
			return err
		}
	}
	return nil
}

func composeMap(n *yaml.Node, field string, allowed ...string) (map[string]*yaml.Node, error) {
	out := map[string]*yaml.Node{}
	if n == nil || n.Tag == "!!null" {
		return out, nil
	}
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: expected a mapping", field)
	}
	for i := 0; i < len(n.Content); i += 2 {
		key := n.Content[i].Value
		if len(allowed) > 0 {
			ok := false
			for _, a := range allowed {
				ok = ok || a == key
			}
			if !ok {
				return nil, fmt.Errorf("%s.%s: unsupported Compose option; configure an equivalent in Hakopod before removing this option", field, key)
			}
		}
		out[key] = n.Content[i+1]
	}
	return out, nil
}
func composeKeys(m map[string]*yaml.Node) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
func composeJSON(n *yaml.Node, out any) error {
	var v any
	if err := n.Decode(&v); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(out)
}
func (r *composeReader) warn(s string) { r.warnings = append(r.warnings, s) }
func (r *composeReader) text(n *yaml.Node, field string) (string, error) {
	if n == nil || n.Tag == "!!null" {
		return "", nil
	}
	if n.Kind != yaml.ScalarNode || (n.Tag != "!!str" && n.Tag != "!!int" && n.Tag != "!!bool" && n.Tag != "!!float") {
		return "", fmt.Errorf("%s: expected a scalar value", field)
	}
	return r.interpolate(n.Value, field)
}

// Supports the common Compose variable forms without consulting os.Environ.
func (r *composeReader) interpolate(s, field string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '$' {
			b.WriteByte(s[i])
			i++
			continue
		}
		i++
		if i < len(s) && s[i] == '$' {
			b.WriteByte('$')
			i++
			continue
		}
		key, op, fallback := "", "", ""
		if i < len(s) && s[i] == '{' {
			end := strings.IndexByte(s[i+1:], '}')
			if end < 0 {
				return "", fmt.Errorf("%s: incomplete variable expression", field)
			}
			body := s[i+1 : i+1+end]
			i += end + 2
			j := 0
			for j < len(body) && (body[j] == '_' || body[j] >= 'a' && body[j] <= 'z' || body[j] >= 'A' && body[j] <= 'Z' || body[j] >= '0' && body[j] <= '9') {
				j++
			}
			key = body[:j]
			rest := body[j:]
			for _, candidate := range []string{":-", ":?", ":+", "-", "?", "+"} {
				if strings.HasPrefix(rest, candidate) {
					op = candidate
					fallback = rest[len(op):]
					break
				}
			}
			if rest != "" && op == "" || strings.ContainsAny(fallback, "${}") {
				return "", fmt.Errorf("%s: nested or unsupported variable expression", field)
			}
		} else {
			start := i
			for i < len(s) && (s[i] == '_' || s[i] >= 'a' && s[i] <= 'z' || s[i] >= 'A' && s[i] <= 'Z' || s[i] >= '0' && s[i] <= '9') {
				i++
			}
			key = s[start:i]
		}
		if !envPattern.MatchString(key) {
			return "", fmt.Errorf("%s: invalid variable expression; escape literal dollars as $$", field)
		}
		v, present := r.variables[key]
		set := present && (!strings.HasPrefix(op, ":") || v != "")
		switch op {
		case "":
			if !present {
				return "", fmt.Errorf("%s: supply variable %s; host environment values are never read", field, key)
			}
		case "-", ":-":
			if !set {
				v = fallback
			}
		case "?", ":?":
			if !set {
				return "", fmt.Errorf("%s: supply required variable %s", field, key)
			}
		case "+", ":+":
			if set {
				v = fallback
			} else {
				v = ""
			}
		}
		b.WriteString(v)
		if b.Len() > MaxBytes {
			return "", fmt.Errorf("%s: expanded value is too large", field)
		}
	}
	return b.String(), nil
}

func (r *composeReader) service(name string, node *yaml.Node) (Service, error) {
	f := "services." + name
	if !namePattern.MatchString(name) {
		return Service{}, fmt.Errorf("%s: use lowercase letters, digits or hyphens; update references too", f)
	}
	m, err := composeMap(node, f, "image", "command", "entrypoint", "environment", "env_file", "depends_on", "ports", "expose", "networks", "volumes", "deploy", "restart", "container_name", "working_dir", "user", "read_only", "stop_grace_period", "platform", "x-hakopod")
	if err != nil {
		return Service{}, err
	}
	s := Service{Size: "small", Replicas: 1, Env: map[string]string{}}
	if s.Image, err = r.text(m["image"], f+".image"); err != nil {
		return s, err
	}
	if s.Image == "" {
		return s, fmt.Errorf("%s.image: provide a built container image; import Dockerfile builds through Git", f)
	}
	if s.Command, err = r.argv(m["entrypoint"], f+".entrypoint"); err != nil {
		return s, err
	}
	if s.Args, err = r.argv(m["command"], f+".command"); err != nil {
		return s, err
	}
	if s.WorkingDir, err = r.text(m["working_dir"], f+".working_dir"); err != nil {
		return s, err
	}
	if n := m["read_only"]; n != nil {
		if err = n.Decode(&s.ReadOnlyRootFilesystem); err != nil {
			return s, fmt.Errorf("%s.read_only: expected a boolean", f)
		}
	}
	if n := m["user"]; n != nil {
		v, e := r.text(n, f+".user")
		if e != nil {
			return s, e
		}
		parts := strings.Split(v, ":")
		if len(parts) > 2 {
			return s, fmt.Errorf("%s.user: use a numeric non-root UID and optional GID", f)
		}
		s.RunAsUser, e = strconv.ParseInt(parts[0], 10, 64)
		if e != nil || s.RunAsUser <= 0 {
			return s, fmt.Errorf("%s.user: use a numeric non-root UID", f)
		}
		if len(parts) == 2 {
			s.RunAsGroup, e = strconv.ParseInt(parts[1], 10, 64)
			if e != nil || s.RunAsGroup <= 0 {
				return s, fmt.Errorf("%s.user: use a numeric non-root GID", f)
			}
		}
	}
	if n := m["platform"]; n != nil {
		v, e := r.text(n, f+".platform")
		if e != nil {
			return s, e
		}
		if v != "linux/amd64" && v != "linux/arm64" {
			return s, fmt.Errorf("%s.platform: use linux/amd64 or linux/arm64", f)
		}
		s.Architecture = strings.TrimPrefix(v, "linux/")
	}
	if m["stop_grace_period"] != nil {
		return s, fmt.Errorf("%s.stop_grace_period: set termination_grace_seconds in x-hakopod instead", f)
	}
	if n := m["restart"]; n != nil {
		v, e := r.text(n, f+".restart")
		if e != nil {
			return s, e
		}
		if v != "always" && v != "unless-stopped" {
			return s, fmt.Errorf("%s.restart: one-shot/on-failure services need an explicit Hakopod job configuration", f)
		}
		r.warn(f + ": Hakopod restarts long-running services; use Stop service to suspend them.")
	}
	if m["container_name"] != nil {
		r.warn(f + ": container_name is replaced by the service name for private DNS; update clients that use the container name.")
	}
	if n := m["deploy"]; n != nil {
		dm, e := composeMap(n, f+".deploy", "replicas")
		if e != nil {
			return s, e
		}
		if dm["replicas"] != nil {
			v, e := r.text(dm["replicas"], f+".deploy.replicas")
			if e != nil {
				return s, e
			}
			i, e := strconv.ParseInt(v, 10, 32)
			if e != nil || i < 1 || i > 20 {
				return s, fmt.Errorf("%s.deploy.replicas: use 1–20", f)
			}
			s.Replicas = int32(i)
		}
	}
	if n := m["networks"]; n != nil {
		if n.Kind == yaml.SequenceNode {
			s.Networks, err = r.list(n, f+".networks")
		} else {
			nm, e := composeMap(n, f+".networks")
			if e != nil {
				return s, e
			}
			s.Networks = []string{}
			for _, key := range composeKeys(nm) {
				opts, e := composeMap(nm[key], f+".networks."+key)
				if e != nil {
					return s, e
				}
				if len(opts) > 0 {
					return s, fmt.Errorf("%s.networks.%s: aliases and per-network addressing require manual configuration", f, key)
				}
				s.Networks = append(s.Networks, key)
			}
		}
		if err != nil {
			return s, err
		}
	}
	if n := m["depends_on"]; n != nil {
		if n.Kind != yaml.SequenceNode {
			return s, fmt.Errorf("%s.depends_on: conditional dependencies require manual readiness/job configuration; use a list for readiness-ordered services", f)
		}
		s.DependsOn, err = r.list(n, f+".depends_on")
		if err != nil {
			return s, err
		}
		r.warn(f + ": dependencies deploy in readiness order, rather than only waiting for container startup.")
	}
	if err = r.ports(&s, m["ports"], f+".ports", true); err != nil {
		return s, err
	}
	if err = r.ports(&s, m["expose"], f+".expose", false); err != nil {
		return s, err
	}
	if n := m["volumes"]; n != nil {
		if n.Kind != yaml.SequenceNode {
			return s, fmt.Errorf("%s.volumes: expected a list", f)
		}
		for _, item := range n.Content {
			mount, e := r.mount(item, f+".volumes")
			if e != nil {
				return s, e
			}
			s.Mounts = append(s.Mounts, mount)
		}
	}
	// Explicit Hakopod options supply settings Compose cannot express faithfully.
	if n := m["x-hakopod"]; n != nil {
		var ext struct {
			Port                    *int32               `json:"port"`
			Public                  *bool                `json:"public"`
			Size                    string               `json:"size"`
			Secrets                 map[string]SecretRef `json:"secrets"`
			Healthcheck             string               `json:"healthcheck"`
			PublicTCP               []PublicTCPListener  `json:"public_tcp"`
			Readiness               *Readiness           `json:"readiness"`
			FSGroup                 int64                `json:"fs_group"`
			TerminationGraceSeconds int64                `json:"termination_grace_seconds"`
		}
		if composeJSON(n, &ext) != nil {
			return s, fmt.Errorf("%s.x-hakopod: invalid or unknown Hakopod option; use port, public, size, secrets, healthcheck, public_tcp, readiness, fs_group or termination_grace_seconds", f)
		}
		if ext.Port != nil {
			if s.Port != 0 && s.Port != *ext.Port {
				s.Ports = append(s.Ports, Port{Name: fmt.Sprintf("p%d-tcp", s.Port), Port: s.Port, TargetPort: s.Port, Protocol: "TCP"})
			}
			s.Port = *ext.Port
			ports := s.Ports[:0]
			for _, p := range s.Ports {
				if p.Port != s.Port || p.Protocol != "TCP" {
					ports = append(ports, p)
				}
			}
			s.Ports = ports
		}
		if ext.Public != nil {
			s.Public = *ext.Public
		}
		if ext.Size != "" {
			s.Size = ext.Size
		}
		s.Secrets = ext.Secrets
		s.Healthcheck = ext.Healthcheck
		s.PublicTCP = ext.PublicTCP
		s.Readiness = ext.Readiness
		s.FSGroup = ext.FSGroup
		s.TerminationGraceSeconds = ext.TerminationGraceSeconds
	}
	if n := m["environment"]; n != nil {
		env := map[string]*yaml.Node{}
		if n.Kind == yaml.SequenceNode {
			for _, item := range n.Content {
				if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
					return s, fmt.Errorf("%s.environment: expected KEY=value strings", f)
				}
				key, v, ok := strings.Cut(item.Value, "=")
				if !ok {
					v = "${" + key + "}"
				}
				if _, exists := env[key]; exists {
					return s, fmt.Errorf("%s.environment: duplicate variable", f)
				}
				env[key] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
			}
		} else {
			env, err = composeMap(n, f+".environment")
			if err != nil {
				return s, err
			}
		}
		for _, key := range composeKeys(env) {
			if !envPattern.MatchString(key) {
				return s, fmt.Errorf("%s.environment: invalid variable name", f)
			}
			if _, ok := s.Secrets[key]; ok {
				r.warn(f + ".environment." + key + ": replaced by the explicit application secret reference; the pasted value is not retained.")
				continue
			}
			n := env[key]
			if n.Tag == "!!null" {
				n = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "${" + key + "}"}
			}
			v, e := r.text(n, f+".environment."+key)
			if e != nil {
				return s, e
			}
			if sensitiveEnv(key, v) {
				return s, fmt.Errorf("%s.environment.%s: bind an application secret in x-hakopod.secrets.%s; secret values cannot be saved in configuration", f, key, key)
			}
			s.Env[key] = v
		}
	}
	if node := m["env_file"]; node != nil {
		var value any
		if node.Decode(&value) != nil {
			return s, errors.New("env_file: invalid filename list")
		}
		if _, err := EnvironmentFilePaths(value); err != nil {
			return s, err
		}
		r.envRefs[name] = value
	}

	return s, nil
}

func (r *composeReader) list(n *yaml.Node, field string) ([]string, error) {
	if n.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s: expected a list", field)
	}
	out := []string{}
	for _, v := range n.Content {
		s, e := r.text(v, field)
		if e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	return out, nil
}

func (r *composeReader) argv(n *yaml.Node, field string) ([]string, error) {
	if n == nil || n.Tag == "!!null" {
		return nil, nil
	}
	if n.Kind == yaml.SequenceNode {
		v, e := r.list(n, field)
		if e == nil && len(v) == 0 {
			return nil, fmt.Errorf("%s: clearing the image command is unsupported; supply an explicit executable or arguments", field)
		}
		return v, e
	}
	s, err := r.text(n, field)
	if err != nil {
		return nil, err
	}
	// Compose command strings are split into argv, not run by a shell.
	var out []string
	var word strings.Builder
	var quote rune
	escaped, started := false, false
	for _, c := range s {
		if escaped {
			word.WriteRune(c)
			escaped = false
			started = true
			continue
		}
		if c == '\\' && quote != '\'' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				word.WriteRune(c)
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			started = true
			continue
		}
		if unicode.IsSpace(c) {
			if started {
				out = append(out, word.String())
				word.Reset()
				started = false
			}
			continue
		}
		word.WriteRune(c)
		started = true
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("%s: unmatched quote or escape; use an argv list", field)
	}
	if started {
		out = append(out, word.String())
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: use an explicit executable or arguments instead of an empty override", field)
	}
	return out, nil
}

func (r *composeReader) ports(s *Service, n *yaml.Node, field string, published bool) error {
	if n == nil {
		return nil
	}
	if n.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s: expected a list", field)
	}
	for _, item := range n.Content {
		protocol, target, host := "TCP", "", ""
		if item.Kind == yaml.MappingNode {
			m, e := composeMap(item, field, "target", "published", "protocol", "host_ip", "mode")
			if e != nil {
				return e
			}
			target, e = r.text(m["target"], field+".target")
			if e != nil {
				return e
			}
			host, e = r.text(m["published"], field+".published")
			if e != nil {
				return e
			}
			if m["protocol"] != nil {
				protocol, e = r.text(m["protocol"], field+".protocol")
				if e != nil {
					return e
				}
				protocol = strings.ToUpper(protocol)
			}
			if m["host_ip"] != nil || m["mode"] != nil {
				r.warn(field + ": host binding addresses and Compose publishing modes do not carry over. Review the explicit HTTP/TCP exposure settings.")
			}
		} else {
			v, e := r.text(item, field)
			if e != nil {
				return e
			}
			base, p, ok := strings.Cut(v, "/")
			if ok {
				protocol = strings.ToUpper(p)
			}
			parts := strings.Split(base, ":")
			if len(parts) > 3 {
				return fmt.Errorf("%s: IPv6 bindings and port ranges require manual configuration", field)
			}
			target = parts[len(parts)-1]
			if len(parts) > 1 {
				host = strings.Join(parts[:len(parts)-1], ":")
			}
		}
		port, e := strconv.ParseInt(target, 10, 32)
		if e != nil || port < 1 || port > 65535 || (protocol != "TCP" && protocol != "UDP") {
			return fmt.Errorf("%s: use individual TCP/UDP ports between 1 and 65535; ranges are unsupported", field)
		}
		if host != "" || published {
			r.warn(fmt.Sprintf("%s: Compose host publishing for container port %d/%s is not copied. Ports default to private unless x-hakopod explicitly enables HTTP or public_tcp on a provisioned self-hosted/BYO listener.", field, port, protocol))
		}
		duplicate := false
		for _, p := range ServicePorts(*s) {
			duplicate = duplicate || (p.Port == int32(port) && p.Protocol == protocol)
		}
		if duplicate {
			continue
		}
		if s.Port == 0 && protocol == "TCP" {
			s.Port = int32(port)
		} else {
			s.Ports = append(s.Ports, Port{Name: fmt.Sprintf("p%d-%s", port, strings.ToLower(protocol)), Port: int32(port), TargetPort: int32(port), Protocol: protocol})
		}
	}
	return nil
}

func (r *composeReader) mount(n *yaml.Node, field string) (Mount, error) {
	var mount Mount
	if n.Kind == yaml.MappingNode {
		m, e := composeMap(n, field, "type", "source", "target", "read_only")
		if e != nil {
			return mount, e
		}
		kind, e := r.text(m["type"], field+".type")
		if e != nil {
			return mount, e
		}
		if kind != "volume" {
			return mount, fmt.Errorf("%s: only named volumes are supported; host bind mounts are not imported", field)
		}
		mount.Volume, e = r.text(m["source"], field+".source")
		if e != nil {
			return mount, e
		}
		mount.MountPath, e = r.text(m["target"], field+".target")
		if e != nil {
			return mount, e
		}
		if m["read_only"] != nil && m["read_only"].Decode(&mount.ReadOnly) != nil {
			return mount, fmt.Errorf("%s.read_only: expected a boolean", field)
		}
	} else {
		v, e := r.text(n, field)
		if e != nil {
			return mount, e
		}
		parts := strings.Split(v, ":")
		if len(parts) < 2 || len(parts) > 3 {
			return mount, fmt.Errorf("%s: use named-volume:/data[:ro]; anonymous and host mounts require manual configuration", field)
		}
		mount.Volume, mount.MountPath = parts[0], parts[1]
		if len(parts) == 3 {
			if parts[2] != "ro" && parts[2] != "rw" {
				return mount, fmt.Errorf("%s: only ro/rw volume modes are supported", field)
			}
			mount.ReadOnly = parts[2] == "ro"
		}
	}
	if !namePattern.MatchString(mount.Volume) {
		return mount, fmt.Errorf("%s: use a declared named volume; host paths are not imported", field)
	}
	return mount, nil
}

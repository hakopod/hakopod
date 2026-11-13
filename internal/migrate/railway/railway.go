// Package railway reads a Railway project and environment into a
// migrate.Inventory through Railway's public GraphQL API. It only queries;
// it never runs a mutation.
package railway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/migrate"
	"github.com/hakopod/hakopod/internal/spec"
)

const endpoint = "https://backboard.railway.com/graphql/v2"

// ErrUnauthorized is returned when Railway refuses the token, or the token
// cannot see the requested project.
var ErrUnauthorized = errors.New("railway: the token cannot read this project")

// maxBody bounds every response read from Railway.
const maxBody = 8 << 20

type tokenKind int

const (
	unknownToken tokenKind = iota
	accountToken           // account or workspace token: Authorization: Bearer
	projectToken           // project token: Project-Access-Token
)

type Client struct {
	token    string
	endpoint string
	http     *http.Client
	kind     tokenKind
	// Set when the token is a project token.
	tokenProject, tokenEnvironment string
}

type Option func(*Client)

// WithBaseURL replaces the GraphQL endpoint, for tests.
func WithBaseURL(u string) Option { return func(c *Client) { c.endpoint = u } }

// WithHTTPClient replaces the HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithProjectToken says whether the token is a project token, skipping
// detection.
func WithProjectToken(project bool) Option {
	return func(c *Client) {
		c.kind = accountToken
		if project {
			c.kind = projectToken
		}
	}
}

func New(token string, opts ...Option) *Client {
	c := &Client{token: token, endpoint: endpoint, http: &http.Client{Timeout: time.Minute}}
	for _, o := range opts {
		o(c)
	}
	return c
}

type gqlError struct {
	Message string `json:"message"`
}

func (c *Client) query(ctx context.Context, kind tokenKind, q string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": q, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if kind == projectToken {
		req.Header.Set("Project-Access-Token", c.token)
	} else {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("railway: %w", c.scrub(err))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("railway: reading the response: %w", c.scrub(err))
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ErrUnauthorized
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("railway: status %d", resp.StatusCode)
		}
		return errors.New("railway: the response is not valid JSON")
	}
	if len(envelope.Errors) > 0 {
		msg := envelope.Errors[0].Message
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "not authorized") || strings.Contains(lower, "unauthorized") || strings.Contains(lower, "forbidden") {
			return ErrUnauthorized
		}
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("railway: %s", c.scrub(errors.New(msg)))
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("railway: status %d", resp.StatusCode)
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("railway: unexpected response shape: %w", err)
	}
	return nil
}

// scrub removes the token from an error text, in case a transport error
// ever echoes a header.
func (c *Client) scrub(err error) error {
	if c.token == "" || !strings.Contains(err.Error(), c.token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), c.token, "[token]"))
}

// detect works out the token kind: a project token answers projectToken
// with the Project-Access-Token header; anything else is sent as Bearer.
func (c *Client) detect(ctx context.Context) error {
	if c.kind == accountToken {
		return nil
	}
	var out struct {
		ProjectToken *struct {
			ProjectID     string `json:"projectId"`
			EnvironmentID string `json:"environmentId"`
		} `json:"projectToken"`
	}
	err := c.query(ctx, projectToken, `query { projectToken { projectId environmentId } }`, nil, &out)
	if err == nil && out.ProjectToken != nil && out.ProjectToken.ProjectID != "" {
		c.kind, c.tokenProject, c.tokenEnvironment = projectToken, out.ProjectToken.ProjectID, out.ProjectToken.EnvironmentID
		return nil
	}
	if c.kind == projectToken {
		if err == nil {
			err = ErrUnauthorized
		}
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	c.kind = accountToken
	return nil
}

type edges[T any] struct {
	Edges []struct {
		Node T `json:"node"`
	} `json:"edges"`
}

func (e edges[T]) nodes() []T {
	out := make([]T, 0, len(e.Edges))
	for _, x := range e.Edges {
		out = append(out, x.Node)
	}
	return out
}

type projectNode struct {
	ID           string                     `json:"id"`
	Name         string                     `json:"name"`
	Environments edges[migrate.Environment] `json:"environments"`
}

func (p projectNode) project(team string) migrate.Project {
	return migrate.Project{ID: p.ID, Name: p.Name, Team: team, Environments: p.Environments.nodes()}
}

const projectFields = `id name environments { edges { node { id name } } }`

// ListProjects lists the projects the token can read: personal projects and
// the projects of every workspace the account belongs to. A project token
// lists only its own project and environment.
func (c *Client) ListProjects(ctx context.Context) ([]migrate.Project, error) {
	if err := c.detect(ctx); err != nil {
		return nil, err
	}
	if c.kind == projectToken {
		var out struct {
			Project projectNode `json:"project"`
		}
		if err := c.query(ctx, c.kind, `query($id: String!) { project(id: $id) { `+projectFields+` } }`, map[string]any{"id": c.tokenProject}, &out); err != nil {
			return nil, err
		}
		p := out.Project.project("")
		envs := p.Environments[:0]
		for _, e := range p.Environments {
			if e.ID == c.tokenEnvironment {
				envs = append(envs, e)
			}
		}
		p.Environments = envs
		return []migrate.Project{p}, nil
	}

	seen := map[string]bool{}
	var projects []migrate.Project
	add := func(team string, nodes []projectNode) {
		for _, n := range nodes {
			if !seen[n.ID] {
				seen[n.ID] = true
				projects = append(projects, n.project(team))
			}
		}
	}
	own, err := c.listProjects(ctx, "")
	if err != nil {
		return nil, err
	}
	// Workspace tokens have no user, so me fails for them; their projects
	// came back above.
	var me struct {
		Me struct {
			Workspaces []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"workspaces"`
		} `json:"me"`
	}
	if err := c.query(ctx, c.kind, `query { me { workspaces { id name } } }`, nil, &me); err == nil {
		for _, w := range me.Me.Workspaces {
			nodes, err := c.listProjects(ctx, w.ID)
			if err != nil {
				return nil, err
			}
			add(w.Name, nodes)
		}
	} else if ctx.Err() != nil {
		return nil, err
	}
	add("", own)
	return projects, nil
}

func (c *Client) listProjects(ctx context.Context, workspaceID string) ([]projectNode, error) {
	const q = `query($after: String, $w: String) {
  projects(first: 100, after: $after, workspaceId: $w) {
    edges { node { ` + projectFields + ` } }
    pageInfo { hasNextPage endCursor }
  }
}`
	vars := map[string]any{"after": nil, "w": nil}
	if workspaceID != "" {
		vars["w"] = workspaceID
	}
	var all []projectNode
	for range 50 {
		var out struct {
			Projects struct {
				edges[projectNode]
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"projects"`
		}
		if err := c.query(ctx, c.kind, q, vars, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Projects.nodes()...)
		if !out.Projects.PageInfo.HasNextPage || out.Projects.PageInfo.EndCursor == "" {
			break
		}
		vars["after"] = out.Projects.PageInfo.EndCursor
	}
	return all, nil
}

type volumeNode struct {
	Name            string `json:"name"`
	VolumeInstances edges[struct {
		MountPath     string `json:"mountPath"`
		ServiceID     string `json:"serviceId"`
		EnvironmentID string `json:"environmentId"`
	}] `json:"volumeInstances"`
}

type instance struct {
	Source *struct {
		Repo  string `json:"repo"`
		Image string `json:"image"`
	} `json:"source"`
	StartCommand     string                          `json:"startCommand"`
	BuildCommand     string                          `json:"buildCommand"`
	RootDirectory    string                          `json:"rootDirectory"`
	HealthcheckPath  string                          `json:"healthcheckPath"`
	NumReplicas      int                             `json:"numReplicas"`
	CronSchedule     string                          `json:"cronSchedule"`
	LatestDeployment *struct{ Meta json.RawMessage } `json:"latestDeployment"`
}

type domain struct {
	Domain     string `json:"domain"`
	TargetPort int    `json:"targetPort"`
}

type serviceData struct {
	id, name string
	inst     instance
	domains  struct {
		ServiceDomains []domain `json:"serviceDomains"`
		CustomDomains  []domain `json:"customDomains"`
	}
	branch               string
	unrendered, rendered map[string]string
	volumes              []string
	engine               migrate.Engine
	version              string
}

const serviceQuery = `query($p: String!, $e: String!, $s: String!) {
  serviceInstance(serviceId: $s, environmentId: $e) {
    source { repo image }
    startCommand buildCommand rootDirectory healthcheckPath numReplicas cronSchedule
    latestDeployment { meta }
  }
  domains(projectId: $p, environmentId: $e, serviceId: $s) {
    serviceDomains { domain targetPort }
    customDomains { domain targetPort }
  }
  deploymentTriggers(projectId: $p, environmentId: $e, serviceId: $s) {
    edges { node { branch repository provider } }
  }
  unrendered: variables(projectId: $p, environmentId: $e, serviceId: $s, unrendered: true)
  rendered: variables(projectId: $p, environmentId: $e, serviceId: $s, unrendered: false)
}`

// Read reads one environment of a project. An empty environmentID picks
// the production environment, or the project token's environment.
func (c *Client) Read(ctx context.Context, projectID, environmentID string) (migrate.Inventory, error) {
	if err := c.detect(ctx); err != nil {
		return migrate.Inventory{}, err
	}
	if c.kind == projectToken {
		if projectID != c.tokenProject {
			return migrate.Inventory{}, ErrUnauthorized
		}
		if environmentID == "" {
			environmentID = c.tokenEnvironment
		}
	}
	var out struct {
		Project *struct {
			ID           string                     `json:"id"`
			Name         string                     `json:"name"`
			Environments edges[migrate.Environment] `json:"environments"`
			Services     edges[struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}] `json:"services"`
			Volumes edges[volumeNode] `json:"volumes"`
		} `json:"project"`
	}
	q := `query($id: String!) { project(id: $id) { id name
  environments { edges { node { id name } } }
  services { edges { node { id name } } }
  volumes { edges { node { name volumeInstances { edges { node { mountPath serviceId environmentId } } } } } }
} }`
	if err := c.query(ctx, c.kind, q, map[string]any{"id": projectID}, &out); err != nil {
		return migrate.Inventory{}, err
	}
	if out.Project == nil {
		return migrate.Inventory{}, ErrUnauthorized
	}
	p := out.Project
	inv := migrate.Inventory{Provider: migrate.Railway, ProjectID: p.ID, ProjectName: p.Name, Services: []migrate.Service{}, Databases: []migrate.Database{}}
	for _, e := range p.Environments.nodes() {
		if e.ID == environmentID || (environmentID == "" && e.Name == "production") {
			environmentID, inv.Environment = e.ID, e.Name
		}
	}
	if inv.Environment == "" {
		return migrate.Inventory{}, fmt.Errorf("railway: project %s has no environment %q", p.Name, environmentID)
	}

	var services []*serviceData
	for _, s := range p.Services.nodes() {
		sd := &serviceData{id: s.ID, name: s.Name}
		var res struct {
			ServiceInstance *instance `json:"serviceInstance"`
			Domains         *struct {
				ServiceDomains []domain `json:"serviceDomains"`
				CustomDomains  []domain `json:"customDomains"`
			} `json:"domains"`
			DeploymentTriggers edges[struct {
				Branch string `json:"branch"`
			}] `json:"deploymentTriggers"`
			Unrendered map[string]string `json:"unrendered"`
			Rendered   map[string]string `json:"rendered"`
		}
		if err := c.query(ctx, c.kind, serviceQuery, map[string]any{"p": p.ID, "e": environmentID, "s": s.ID}, &res); err != nil {
			return migrate.Inventory{}, fmt.Errorf("reading service %s: %w", s.Name, err)
		}
		if res.ServiceInstance == nil {
			continue // the service is not deployed in this environment
		}
		sd.inst, sd.unrendered, sd.rendered = *res.ServiceInstance, res.Unrendered, res.Rendered
		if res.Domains != nil {
			sd.domains.ServiceDomains, sd.domains.CustomDomains = res.Domains.ServiceDomains, res.Domains.CustomDomains
		}
		for _, t := range res.DeploymentTriggers.nodes() {
			if t.Branch != "" {
				sd.branch = t.Branch
				break
			}
		}
		if sd.branch == "" && sd.inst.LatestDeployment != nil {
			var meta struct {
				Branch string `json:"branch"`
			}
			_ = json.Unmarshal(sd.inst.LatestDeployment.Meta, &meta)
			sd.branch = meta.Branch
		}
		for _, v := range p.Volumes.nodes() {
			for _, vi := range v.VolumeInstances.nodes() {
				if vi.ServiceID == s.ID && vi.EnvironmentID == environmentID {
					sd.volumes = append(sd.volumes, vi.MountPath)
				}
			}
		}
		if sd.inst.Source != nil {
			sd.engine, sd.version = databaseImage(sd.inst.Source.Image)
		}
		services = append(services, sd)
	}

	databases := map[string]bool{}
	for _, sd := range services {
		if sd.engine == "" {
			continue
		}
		databases[sd.name] = true
		db := migrate.Database{Name: sd.name, Engine: sd.engine, Version: sd.version}
		if u := sd.rendered[publicURLVar[sd.engine]]; u != "" {
			db.URL, db.Reachable = u, true
		} else {
			inv.Warnings = append(inv.Warnings, fmt.Sprintf("Database %s has no public TCP proxy on Railway, so its data cannot be copied. Enable a TCP proxy on it, or start with an empty database.", sd.name))
		}
		inv.Databases = append(inv.Databases, db)
	}

	for _, sd := range services {
		if sd.engine != "" {
			continue
		}
		svc, warnings, ok := sd.service(databases)
		inv.Warnings = append(inv.Warnings, warnings...)
		if ok {
			inv.Services = append(inv.Services, svc)
		}
	}
	return inv, nil
}

var publicURLVar = map[migrate.Engine]string{
	migrate.PostgreSQL: "DATABASE_PUBLIC_URL",
	migrate.Redis:      "REDIS_PUBLIC_URL",
	migrate.MySQL:      "MYSQL_PUBLIC_URL",
	migrate.MongoDB:    "MONGO_PUBLIC_URL",
}

// databaseImage recognises Railway's database images, including the
// railwayapp-templates ones, and the major version from the tag.
func databaseImage(image string) (migrate.Engine, string) {
	if image == "" {
		return "", ""
	}
	name, tag, _ := strings.Cut(image[strings.LastIndex(image, "/")+1:], ":")
	tag, _, _ = strings.Cut(tag, "@")
	var engine migrate.Engine
	switch name = strings.ToLower(name); {
	case strings.HasPrefix(name, "postgres"), strings.HasPrefix(name, "postgis"), strings.HasPrefix(name, "timescaledb"):
		engine = migrate.PostgreSQL
	case strings.HasPrefix(name, "redis"):
		engine = migrate.Redis
	case strings.HasPrefix(name, "mysql"):
		engine = migrate.MySQL
	case strings.HasPrefix(name, "mongo"):
		engine = migrate.MongoDB
	default:
		return "", ""
	}
	major := tag
	if i := strings.IndexAny(major, ".-"); i >= 0 {
		major = major[:i]
	}
	if _, err := strconv.Atoi(major); err != nil {
		major = ""
	}
	return engine, major
}

var (
	wholeRef = regexp.MustCompile(`^\$\{\{\s*([^.}\s]+)\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}$`)
	anyRef   = regexp.MustCompile(`\$\{\{\s*([^.}\s]+)\.[A-Za-z_][A-Za-z0-9_]*\s*\}\}`)
)

// refPart maps the variables Railway database services publish to the part
// of the connection they carry.
var refPart = map[string]string{
	"DATABASE_URL": "url", "DATABASE_PRIVATE_URL": "url", "DATABASE_PUBLIC_URL": "url",
	"REDIS_URL": "url", "REDIS_PRIVATE_URL": "url", "REDIS_PUBLIC_URL": "url",
	"MYSQL_URL": "url", "MYSQL_PRIVATE_URL": "url", "MYSQL_PUBLIC_URL": "url",
	"MONGO_URL": "url", "MONGO_PRIVATE_URL": "url", "MONGO_PUBLIC_URL": "url",
	"PGHOST": "host", "REDISHOST": "host", "MYSQLHOST": "host", "MONGOHOST": "host",
	"PGPORT": "port", "REDISPORT": "port", "MYSQLPORT": "port", "MONGOPORT": "port",
	"PGUSER": "user", "POSTGRES_USER": "user", "REDISUSER": "user", "MYSQLUSER": "user", "MONGOUSER": "user", "MONGO_INITDB_ROOT_USERNAME": "user",
	"PGPASSWORD": "password", "POSTGRES_PASSWORD": "password", "REDIS_PASSWORD": "password", "REDISPASSWORD": "password",
	"MYSQLPASSWORD": "password", "MYSQL_ROOT_PASSWORD": "password", "MONGOPASSWORD": "password", "MONGO_INITDB_ROOT_PASSWORD": "password",
	"PGDATABASE": "database", "POSTGRES_DB": "database", "MYSQLDATABASE": "database", "MYSQL_DATABASE": "database",
}

func (sd *serviceData) service(databases map[string]bool) (migrate.Service, []string, bool) {
	var warnings []string
	svc := migrate.Service{
		Name:         sd.name,
		BuildCommand: sd.inst.BuildCommand,
		StartCommand: sd.inst.StartCommand,
		Healthcheck:  sd.inst.HealthcheckPath,
		Replicas:     sd.inst.NumReplicas,
		Variables:    []migrate.Variable{},
		Volumes:      sd.volumes,
	}
	switch src := sd.inst.Source; {
	case src != nil && src.Repo != "":
		owner, name, ok := strings.Cut(strings.TrimSuffix(src.Repo, ".git"), "/")
		if !ok || owner == "" || name == "" {
			return svc, []string{fmt.Sprintf("Service %s deploys from %q, which is not a GitHub repository; it was skipped.", sd.name, src.Repo)}, false
		}
		svc.Repo = &migrate.Repo{Provider: "github", Owner: owner, Name: name, Branch: sd.branch, RootDir: strings.Trim(sd.inst.RootDirectory, "/")}
	case src != nil && src.Image != "":
		svc.Image = src.Image
	default:
		return svc, []string{fmt.Sprintf("Service %s was deployed from a local upload, not a repository or image; it was skipped.", sd.name)}, false
	}

	for name, raw := range sd.unrendered {
		if strings.HasPrefix(name, "RAILWAY_") {
			continue
		}
		value, ok := sd.rendered[name]
		if !ok {
			value = raw
		}
		v := migrate.Variable{Name: name, Value: value, Sensitive: spec.SensitiveEnv(name, value)}
		if m := wholeRef.FindStringSubmatch(raw); m != nil && databases[m[1]] && refPart[m[2]] != "" {
			v.Value, v.DatabaseRef = "", &migrate.DatabaseRef{Database: m[1], Part: refPart[m[2]]}
		} else if refs := anyRef.FindAllStringSubmatch(raw, -1); len(refs) > 0 {
			for _, r := range refs {
				if r[1] != "shared" && r[1] != sd.name {
					warnings = append(warnings, fmt.Sprintf("Variable %s on %s refers to %s on Railway; its current value was copied and will not follow the migrated service.", name, sd.name, r[1]))
					break
				}
			}
		}
		svc.Variables = append(svc.Variables, v)
	}
	slices.SortFunc(svc.Variables, func(a, b migrate.Variable) int { return strings.Compare(a.Name, b.Name) })

	for _, d := range sd.domains.CustomDomains {
		svc.Domains = append(svc.Domains, d.Domain)
	}
	all := append(append([]domain{}, sd.domains.ServiceDomains...), sd.domains.CustomDomains...)
	svc.Public = len(all) > 0
	if port, err := strconv.Atoi(sd.rendered["PORT"]); err == nil && port > 0 {
		svc.Port = port
	} else {
		for _, d := range all {
			if d.TargetPort > 0 {
				svc.Port = d.TargetPort
				break
			}
		}
	}
	if sd.inst.CronSchedule != "" {
		svc.Crons = []migrate.Cron{{Schedule: sd.inst.CronSchedule, Command: sd.inst.StartCommand}}
	}
	for _, v := range sd.volumes {
		warnings = append(warnings, fmt.Sprintf("Service %s has a volume at %s; its data is not copied.", sd.name, v))
	}
	return svc, warnings, true
}

// Package vercel reads a Vercel project's production configuration into a
// migrate.Inventory through Vercel's REST API. It only sends GET requests.
package vercel

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/migrate"
	"github.com/hakopod/hakopod/internal/spec"
)

// ErrUnauthorized is returned when Vercel refuses the token, or the token
// cannot see the requested project.
var ErrUnauthorized = errors.New("vercel: the token cannot read this project")

var errNotFound = errors.New("vercel: not found")

const maxBody = 8 << 20

type Client struct {
	token string
	base  string
	team  string
	http  *http.Client
}

type Option func(*Client)

// WithBaseURL replaces https://api.vercel.com, for tests.
func WithBaseURL(u string) Option { return func(c *Client) { c.base = strings.TrimSuffix(u, "/") } }

// WithHTTPClient replaces the HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithTeam sends teamId on every request, for a team's projects.
func WithTeam(teamID string) Option { return func(c *Client) { c.team = teamID } }

func New(token string, opts ...Option) *Client {
	c := &Client{token: token, base: "https://api.vercel.com", http: &http.Client{Timeout: time.Minute}}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	if query == nil {
		query = url.Values{}
	}
	if c.team != "" {
		query.Set("teamId", c.team)
	}
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("vercel: %w", c.scrub(err))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("vercel: reading the response: %w", c.scrub(err))
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ErrUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return errNotFound
	case resp.StatusCode != http.StatusOK:
		// Keep only Vercel's error code; the message can echo the request.
		var e struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		text := fmt.Sprintf("vercel: GET %s: status %d", path, resp.StatusCode)
		if e.Error.Code != "" && len(e.Error.Code) < 64 {
			text += " (" + e.Error.Code + ")"
		}
		return errors.New(text)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("vercel: unexpected response from %s: %w", path, err)
	}
	return nil
}

func (c *Client) scrub(err error) error {
	if c.token == "" || !strings.Contains(err.Error(), c.token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), c.token, "[token]"))
}

type team struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

func (c *Client) teams(ctx context.Context) ([]team, error) {
	if c.team != "" {
		return nil, nil
	}
	var out struct {
		Teams []team `json:"teams"`
	}
	if err := c.get(ctx, "/v2/teams", url.Values{"limit": {"100"}}, &out); err != nil {
		return nil, err
	}
	return out.Teams, nil
}

// ListProjects lists the token user's own projects and those of each team
// they belong to. With WithTeam it lists that team's projects only.
func (c *Client) ListProjects(ctx context.Context) ([]migrate.Project, error) {
	teams, err := c.teams(ctx)
	if err != nil {
		return nil, err
	}
	projects, err := c.listProjects(ctx, "")
	// A team-scoped token cannot list personal projects.
	if errors.Is(err, ErrUnauthorized) && len(teams) > 0 {
		projects, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, t := range teams {
		tc := *c
		tc.team = t.ID
		more, err := tc.listProjects(ctx, cmp.Or(t.Name, t.Slug))
		if err != nil {
			return nil, err
		}
		projects = append(projects, more...)
	}
	return projects, nil
}

func (c *Client) listProjects(ctx context.Context, teamName string) ([]migrate.Project, error) {
	var projects []migrate.Project
	q := url.Values{"limit": {"100"}}
	for range 100 {
		var out struct {
			Projects []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"projects"`
			Pagination struct {
				Next any `json:"next"`
			} `json:"pagination"`
		}
		if err := c.get(ctx, "/v10/projects", q, &out); err != nil {
			return nil, err
		}
		for _, p := range out.Projects {
			projects = append(projects, migrate.Project{ID: p.ID, Name: p.Name, Team: teamName})
		}
		// next is a timestamp or a continuation token, passed back as from.
		var next string
		switch v := out.Pagination.Next.(type) {
		case float64:
			next = strconv.FormatFloat(v, 'f', -1, 64)
		case string:
			next = v
		}
		if next == "" || len(out.Projects) == 0 {
			break
		}
		q = url.Values{"limit": {"100"}, "from": {next}}
	}
	return projects, nil
}

type project struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Framework       string `json:"framework"`
	RootDirectory   string `json:"rootDirectory"`
	BuildCommand    string `json:"buildCommand"`
	InstallCommand  string `json:"installCommand"`
	OutputDirectory string `json:"outputDirectory"`
	Link            *struct {
		Type             string `json:"type"`
		Org              string `json:"org"`
		Repo             string `json:"repo"`
		ProductionBranch string `json:"productionBranch"`
	} `json:"link"`
	Crons *struct {
		Definitions []struct {
			Path     string `json:"path"`
			Schedule string `json:"schedule"`
		} `json:"definitions"`
	} `json:"crons"`
}

// targets accepts Vercel's target as a string or a list.
type targets []string

func (t *targets) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*t = targets{one}
		return nil
	}
	return json.Unmarshal(b, (*[]string)(t))
}

type env struct {
	ID     string  `json:"id"`
	Key    string  `json:"key"`
	Type   string  `json:"type"`
	Target targets `json:"target"`
}

// Read reads a project's production configuration. environmentID is
// ignored: Vercel projects always migrate production. Without WithTeam,
// a project that is not the user's own is looked up in each team.
func (c *Client) Read(ctx context.Context, projectID, _ string) (migrate.Inventory, error) {
	var p project
	rc := *c
	err := rc.get(ctx, "/v9/projects/"+url.PathEscape(projectID), nil, &p)
	if (errors.Is(err, errNotFound) || errors.Is(err, ErrUnauthorized)) && c.team == "" {
		teams, terr := c.teams(ctx)
		if terr != nil {
			return migrate.Inventory{}, terr
		}
		for _, t := range teams {
			rc.team = t.ID
			if err = rc.get(ctx, "/v9/projects/"+url.PathEscape(projectID), nil, &p); err == nil {
				break
			}
		}
	}
	if errors.Is(err, errNotFound) {
		return migrate.Inventory{}, ErrUnauthorized
	}
	if err != nil {
		return migrate.Inventory{}, err
	}
	return rc.read(ctx, p)
}

func (c *Client) read(ctx context.Context, p project) (migrate.Inventory, error) {
	base := "/v9/projects/" + url.PathEscape(p.ID)
	inv := migrate.Inventory{Provider: migrate.Vercel, ProjectID: p.ID, ProjectName: p.Name, Environment: "production", Services: []migrate.Service{}, Databases: []migrate.Database{}}

	var envs struct {
		Envs []env `json:"envs"`
	}
	if err := c.get(ctx, "/v10/projects/"+url.PathEscape(p.ID)+"/env", nil, &envs); err != nil {
		return inv, err
	}
	var vars []migrate.Variable
	for _, e := range envs.Envs {
		if e.Type == "system" || !slices.Contains(e.Target, "production") {
			continue
		}
		v := migrate.Variable{Name: e.Key, Unreadable: true, Sensitive: true}
		if e.Type != "sensitive" && e.Type != "secret" {
			var dec struct {
				Value string `json:"value"`
			}
			err := c.get(ctx, "/v1/projects/"+url.PathEscape(p.ID)+"/env/"+url.PathEscape(e.ID), nil, &dec)
			if err != nil && !errors.Is(err, errNotFound) {
				return inv, err
			}
			if dec.Value != "" {
				v = migrate.Variable{Name: e.Key, Value: dec.Value, Sensitive: spec.SensitiveEnv(e.Key, dec.Value)}
			}
		}
		vars = append(vars, v)
	}
	slices.SortFunc(vars, func(a, b migrate.Variable) int { return strings.Compare(a.Name, b.Name) })
	inv.Databases, inv.Warnings = detectDatabases(vars)

	if p.Link == nil || p.Link.Type != "github" {
		kind := "no Git repository"
		if p.Link != nil {
			kind = "a " + p.Link.Type + " repository"
		}
		inv.Warnings = append(inv.Warnings, fmt.Sprintf("Project %s is connected to %s; Hakopod builds from GitHub only, so it was skipped.", p.Name, kind))
		return inv, nil
	}

	svc := migrate.Service{
		Name:           p.Name,
		Repo:           &migrate.Repo{Provider: "github", Owner: p.Link.Org, Name: p.Link.Repo, Branch: p.Link.ProductionBranch, RootDir: strings.Trim(p.RootDirectory, "/")},
		Framework:      p.Framework,
		InstallCommand: p.InstallCommand,
		BuildCommand:   p.BuildCommand,
		OutputDir:      p.OutputDirectory,
		Port:           3000,
		Public:         true,
		Variables:      vars,
	}
	if svc.Variables == nil {
		svc.Variables = []migrate.Variable{}
	}

	var domains struct {
		Domains []struct {
			Name      string `json:"name"`
			GitBranch string `json:"gitBranch"`
		} `json:"domains"`
	}
	if err := c.get(ctx, base+"/domains", url.Values{"limit": {"100"}}, &domains); err != nil {
		return inv, err
	}
	for _, d := range domains.Domains {
		// Branch domains serve previews; only production is migrated.
		if d.GitBranch == "" && !strings.HasSuffix(strings.ToLower(d.Name), ".vercel.app") {
			svc.Domains = append(svc.Domains, d.Name)
		}
	}
	if p.Crons != nil {
		for _, d := range p.Crons.Definitions {
			svc.Crons = append(svc.Crons, migrate.Cron{Schedule: d.Schedule, Path: d.Path})
		}
	}
	if p.Framework == "nextjs" {
		inv.Warnings = append(inv.Warnings, "Next.js middleware and edge routes run in the Node.js server on Hakopod, and image optimization and ISR use that server's own cache instead of Vercel's CDN.")
	}
	inv.Services = append(inv.Services, svc)
	return inv, nil
}

// Variable names (or suffixes, for integrations with a custom prefix) that
// Vercel storage and marketplace integrations set.
var (
	postgresURLs = []string{"POSTGRES_URL", "POSTGRES_URL_NON_POOLING", "POSTGRES_PRISMA_URL", "POSTGRES_URL_NO_SSL", "DATABASE_URL", "DATABASE_URL_UNPOOLED"}
	postgresPart = map[string]string{
		"POSTGRES_HOST": "host", "PGHOST": "host", "PGHOST_UNPOOLED": "host",
		"POSTGRES_USER": "user", "PGUSER": "user",
		"POSTGRES_PASSWORD": "password", "PGPASSWORD": "password",
		"POSTGRES_DATABASE": "database", "PGDATABASE": "database",
	}
	redisURLs = []string{"KV_URL", "REDIS_URL", "UPSTASH_REDIS_URL"}
	redisREST = []string{"KV_REST_API_URL", "KV_REST_API_TOKEN", "KV_REST_API_READ_ONLY_TOKEN", "UPSTASH_REDIS_REST_URL", "UPSTASH_REDIS_REST_TOKEN"}
)

func named(name string, list []string) bool {
	for _, n := range list {
		if name == n || strings.HasSuffix(name, "_"+n) {
			return true
		}
	}
	return false
}

func part(name string, parts map[string]string) string {
	for n, p := range parts {
		if name == n || strings.HasSuffix(name, "_"+n) {
			return p
		}
	}
	return ""
}

// detectDatabases finds Neon/Vercel Postgres and Upstash/Vercel KV from
// their connection variables, one Database per host, and turns the
// variables into references to it.
func detectDatabases(vars []migrate.Variable) ([]migrate.Database, []string) {
	dbs := []migrate.Database{}
	var warnings []string
	byHost := map[string]int{}
	add := func(engine migrate.Engine, host, value string, direct bool) string {
		i, ok := byHost[host]
		if !ok {
			name, n := "postgres", 1
			if engine == migrate.Redis {
				name = "redis"
			}
			for _, d := range dbs {
				if d.Engine == engine {
					n++
				}
			}
			if n > 1 {
				name += "-" + strconv.Itoa(n)
			}
			dbs = append(dbs, migrate.Database{Name: name, Engine: engine, URL: value, Reachable: true})
			i = len(dbs) - 1
			byHost[host] = i
		}
		// Copy from the direct (non-pooling) endpoint when there is one.
		if direct {
			dbs[i].URL = value
		}
		return dbs[i].Name
	}
	for i := range vars {
		v := &vars[i]
		if v.Value == "" {
			continue
		}
		u, err := url.Parse(v.Value)
		if err != nil || u.Hostname() == "" {
			continue
		}
		switch {
		case (u.Scheme == "postgres" || u.Scheme == "postgresql") && (named(v.Name, postgresURLs) || strings.HasSuffix(u.Hostname(), ".neon.tech")):
			host := strings.Replace(u.Hostname(), "-pooler.", ".", 1)
			direct := host == u.Hostname() || strings.Contains(v.Name, "NON_POOLING") || strings.Contains(v.Name, "UNPOOLED")
			v.DatabaseRef, v.Value = &migrate.DatabaseRef{Database: add(migrate.PostgreSQL, host, v.Value, direct), Part: "url"}, ""
		case (u.Scheme == "redis" || u.Scheme == "rediss") && named(v.Name, redisURLs):
			v.DatabaseRef, v.Value = &migrate.DatabaseRef{Database: add(migrate.Redis, u.Hostname(), v.Value, false), Part: "url"}, ""
		}
	}
	// Separate connection parts can only be tied to a database when there is one.
	var postgres []string
	for _, d := range dbs {
		if d.Engine == migrate.PostgreSQL {
			postgres = append(postgres, d.Name)
		}
	}
	rest := false
	for i := range vars {
		v := &vars[i]
		if p := part(v.Name, postgresPart); p != "" && len(postgres) == 1 && v.DatabaseRef == nil && !v.Unreadable {
			v.DatabaseRef, v.Value = &migrate.DatabaseRef{Database: postgres[0], Part: p}, ""
		}
		rest = rest || named(v.Name, redisREST)
	}
	if rest {
		warnings = append(warnings, "The Upstash REST variables (KV_REST_API_URL and its tokens) were copied as they are; Hakopod's Redis has no REST API, so the app should connect with the Redis URL instead.")
	}
	return dbs, warnings
}

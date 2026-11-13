package migrate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

func options(values map[string]map[string]string) ConvertOptions {
	return ConvertOptions{Project: "demo", Environment: "production", Application: "shop", GitConnectionID: "conn-1", Values: values, SecretKey: testKey}
}

func railway() Inventory {
	return Inventory{Provider: Railway, ProjectName: "shop", Databases: []Database{
		{Name: "Postgres", Engine: PostgreSQL, Version: "16", Reachable: true, URL: "postgres://u:dbsecret@proxy.rlwy.net:5432/railway", SizeBytes: 3 << 30},
		{Name: "Redis", Engine: Redis, Version: "7"},
	}, Services: []Service{
		{Name: "API Server", Repo: &Repo{Provider: "github", Owner: "acme", Name: "shop", Branch: "main", RootDir: "/api"}, BuildCommand: "npm run build", StartCommand: "node dist/index.js", Port: 4000, Public: true, Healthcheck: "/healthz", Replicas: 3,
			Domains: []string{"api.acme.com"}, Volumes: []string{"/data"},
			Variables: []Variable{
				{Name: "DATABASE_URL", DatabaseRef: &DatabaseRef{Database: "Postgres", Part: "url"}},
				{Name: "PGPASSWORD", DatabaseRef: &DatabaseRef{Database: "Postgres", Part: "password"}},
				{Name: "PGUSER", DatabaseRef: &DatabaseRef{Database: "Postgres", Part: "user"}},
				{Name: "REDIS_URL", DatabaseRef: &DatabaseRef{Database: "Redis", Part: "url"}},
				{Name: "STRIPE_SECRET_KEY", Value: "sk_live_railwaysecret"},
				{Name: "LOG_LEVEL", Value: "info"},
			}},
		{Name: "metabase", Image: "metabase/metabase:v0.50.0", Port: 3000,
			Variables: []Variable{{Name: "MB_DB_PASS", Value: "plainlooking", Sensitive: true}}},
		{Name: "nightly", Image: "acme/jobs:1.2", Crons: []Cron{{Schedule: "0 3 * * *", Command: "node cleanup.js"}}, Port: 8080},
	}}
}

func vercel() Inventory {
	return Inventory{Provider: Vercel, ProjectName: "storefront", Databases: []Database{
		{Name: "neon-db", Engine: PostgreSQL, Version: "17", Reachable: true, URL: "postgres://neon:neonsecret@ep.neon.tech/db"},
		{Name: "upstash", Engine: Redis, Reachable: true, URL: "rediss://default:upsecret@up.upstash.io:6379"},
	}, Services: []Service{
		{Name: "storefront", Repo: &Repo{Provider: "github", Owner: "acme", Name: "storefront", Branch: "prod"}, Framework: "nextjs", InstallCommand: "pnpm install", Public: true,
			Domains: []string{"shop.acme.com"}, Crons: []Cron{{Schedule: "*/10 * * * *", Path: "/api/cron/sync"}},
			Variables: []Variable{
				{Name: "POSTGRES_URL", DatabaseRef: &DatabaseRef{Database: "neon-db", Part: "url"}},
				{Name: "POSTGRES_PRISMA_URL", DatabaseRef: &DatabaseRef{Database: "neon-db", Part: "url"}},
				{Name: "KV_URL", DatabaseRef: &DatabaseRef{Database: "upstash", Part: "url"}},
				{Name: "NEXT_PUBLIC_SITE", Value: "https://shop.acme.com"},
				{Name: "CRON_SECRET", Unreadable: true, Sensitive: true},
				{Name: "OPENAI_API_KEY", Unreadable: true, Sensitive: true},
			}},
	}}
}

func TestConvert(t *testing.T) {
	cases := []struct {
		name    string
		inv     Inventory
		values  map[string]map[string]string
		secrets []string // values that must not appear in JSON
		check   func(t *testing.T, p Proposal)
	}{
		{"railway", railway(), nil, []string{"sk_live_railwaysecret", "plainlooking", "dbsecret"}, func(t *testing.T, p Proposal) {
			api := p.Spec.Services["api-server"]
			if api.Image != PlaceholderImage || !api.Suspended || api.Port != 4000 || !api.Public || api.Healthcheck != "/healthz" {
				t.Fatalf("git service placeholder: %+v", api)
			}
			if api.Replicas != 1 || api.Volume == nil || api.Volume.MountPath != "/data" {
				t.Fatalf("volume service must run one replica: %+v", api)
			}
			if api.Env["LOG_LEVEL"] != "info" || api.Env["PORT"] != "4000" || api.Env["PGUSER"] != "app" || api.Secrets["STRIPE_SECRET_KEY"].Ref == "" {
				t.Fatalf("variables: env=%v secrets=%v", api.Env, api.Secrets)
			}
			if _, ok := api.Env["PGPASSWORD"]; ok {
				t.Fatal("password part must not be plain env")
			}
			if len(p.Builds) != 1 || p.Builds[0].Repository != "acme/shop" || p.Builds[0].ContextPath != "api" || p.Builds[0].Mode != "framework" || p.Builds[0].Framework.StartCommand != "node dist/index.js" || p.Builds[0].Framework.Port != 4000 || p.Builds[0].ConnectionID != "conn-1" {
				t.Fatalf("build: %+v %+v", p.Builds, p.Builds[0].Framework)
			}
			if mb := p.Spec.Services["metabase"]; mb.Image != "metabase/metabase:v0.50.0" || mb.Public || mb.Secrets["MB_DB_PASS"].Ref == "" {
				t.Fatalf("image service: %+v", mb)
			}
			n := p.Spec.Services["nightly"]
			if n.Job == nil || n.Job.Schedule.Cron != "0 3 * * *" || n.Port != 0 || strings.Join(n.Command, " ") != "sh -c node cleanup.js" {
				t.Fatalf("cron service: %+v", n)
			}
			if len(p.Databases) != 2 || p.Databases[0].Version != "17" || !p.Databases[0].CopyData || p.Databases[0].Spec.StorageGiB != 6 || p.Databases[1].Version != "8" || p.Databases[1].CopyData {
				t.Fatalf("databases: %+v", p.Databases)
			}
			if p.Domains["api.acme.com"] != "api-server" {
				t.Fatalf("domains: %v", p.Domains)
			}
			if len(p.Bindings) != 2 || p.Bindings[0].Variable != "DATABASE_URL" || p.Bindings[0].Database != "shop-postgres" || p.Bindings[1].Binding.Protocol != "redis" {
				t.Fatalf("bindings: %+v", p.Bindings)
			}
			for _, want := range []string{"no public endpoint", "PGPASSWORD", "volume", "version 16"} {
				if !contains(p.Warnings, want) {
					t.Errorf("missing warning about %q in %q", want, p.Warnings)
				}
			}
		}},
		{"vercel", vercel(), map[string]map[string]string{"storefront": {"CRON_SECRET": "cronsecretvalue"}}, []string{"cronsecretvalue", "neonsecret", "upsecret"}, func(t *testing.T, p Proposal) {
			s := p.Spec.Services["storefront"]
			if s.Port != 3000 || s.Env["NEXT_PUBLIC_SITE"] == "" || s.Secrets["CRON_SECRET"].Ref == "" {
				t.Fatalf("storefront: %+v", s)
			}
			b := p.Builds[0]
			if b.Framework.Framework != "nextjs" || b.Framework.PackageManager != "pnpm" || b.Framework.InstallCommand != "pnpm install" || b.Branch != "prod" || b.BuildArgs["NEXT_PUBLIC_SITE"] == "" {
				t.Fatalf("build: %+v %+v", b, b.Framework)
			}
			if len(p.Missing) != 1 || p.Missing[0] != (MissingVariable{"storefront", "OPENAI_API_KEY"}) {
				t.Fatalf("missing: %+v", p.Missing)
			}
			cron := p.Spec.Services["storefront-cron"]
			if cron.Job == nil || cron.Image != CronImage || cron.Secrets["CRON_SECRET"] != s.Secrets["CRON_SECRET"] || !strings.Contains(cron.Command[2], "http://storefront:3000/api/cron/sync") {
				t.Fatalf("cron job: %+v", cron)
			}
			if len(p.Bindings) != 3 || p.Bindings[2].Database != "shop-upstash" || !p.Databases[1].CopyData || p.Databases[1].Version != "8" {
				t.Fatalf("bindings %+v databases %+v", p.Bindings, p.Databases)
			}
			if len(p.SecretNames) != 1 || p.Secrets[p.SecretNames[0]] != "cronsecretvalue" {
				t.Fatalf("secrets: %v", p.SecretNames)
			}
		}},
		{"collisions", Inventory{Services: []Service{
			{Name: "Web", Image: "nginx:1"}, {Name: "web", Image: "nginx:2"}, {Name: "web!", Image: "nginx:3"},
			{Name: "123", Image: "nginx:4"}, {Name: strings.Repeat("x", 60), Image: "nginx:5"}, {Name: strings.Repeat("x", 60), Image: "nginx:6"},
		}}, nil, nil, func(t *testing.T, p Proposal) {
			for _, name := range []string{"web", "web-2", "web-3", "s-123", strings.Repeat("x", 40), strings.Repeat("x", 38) + "-2"} {
				if _, ok := p.Spec.Services[name]; !ok {
					t.Errorf("no service %q in %v", name, spec.Names(p.Spec))
				}
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Convert(tc.inv, options(tc.values))
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, p)
			// The produced spec passes Hakopod's validator, before and after binding.
			if _, err := spec.Normalize(p.Spec); err != nil {
				t.Fatal(err)
			}
			ids := map[string]string{}
			for _, db := range p.Databases {
				ids[db.Name] = strings.Repeat("a", 32)
				if err := db.Spec.Validate(); err != nil {
					t.Fatal(err)
				}
			}
			bound, err := p.Bind(ids)
			if err != nil {
				t.Fatal(err)
			}
			for _, b := range p.Bindings {
				if bound.Services[b.Service].Bindings[b.Variable].ManagedDatabase != ids[b.Database] {
					t.Fatalf("binding %+v not applied", b)
				}
			}
			data, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range tc.secrets {
				if strings.Contains(string(data), secret) {
					t.Fatalf("secret value %q in proposal JSON", secret)
				}
			}
			for _, value := range p.Secrets {
				if strings.Contains(string(data), value) {
					t.Fatalf("secret value %q in proposal JSON", value)
				}
			}
			again, _ := Convert(tc.inv, options(tc.values))
			if strings.Join(again.SecretNames, ",") != strings.Join(p.SecretNames, ",") {
				t.Fatal("secret names must be deterministic")
			}
		})
	}
}

func TestPickVersion(t *testing.T) {
	for _, tc := range []struct{ source, want string }{{"16", "17"}, {"17", "17"}, {"18", "18"}, {"19", "18"}, {"", "18"}, {"14.5", "17"}} {
		if got := pickVersion(tc.source, []string{"17", "18"}); got != tc.want {
			t.Errorf("pickVersion(%q) = %q, want %q", tc.source, got, tc.want)
		}
	}
}

func contains(list []string, part string) bool {
	for _, s := range list {
		if strings.Contains(s, part) {
			return true
		}
	}
	return false
}

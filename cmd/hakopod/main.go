// Hakopod's small CLI shares the versioned Go API with the dashboard.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

const version = "0.1.0-dev"

type config struct {
	URL         string `json:"url"`
	Key         string `json:"key"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
}
type client struct {
	url, key string
	http     *http.Client
}

func main() {
	code := 0
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "hakopod:", err)
		code = 1
		var e *exitError
		if errors.As(err, &e) {
			code = e.code
		}
	}
	os.Exit(code)
}

type exitError struct {
	code    int
	message string
}

func (e *exitError) Error() string { return e.message }
func run() error {
	if len(os.Args) < 2 {
		help()
		return nil
	}
	command := os.Args[1]
	if command == "version" {
		fmt.Println(version)
		return nil
	}
	if command == "help" || command == "--help" {
		help()
		return nil
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	file := fs.String("file", "hakopod.toml", "application TOML path")
	project := fs.String("project", "", "project context")
	environment := fs.String("environment", "", "environment context")
	service := fs.String("service", "", "service name")
	outputJSON := fs.Bool("json", false, "machine-readable JSON")
	wait := fs.Bool("wait", false, "wait for final deployment outcome")
	idem := fs.String("idempotency-key", "", "stable retry key (generated if omitted)")
	revision := fs.Int64("revision", 0, "successful revision to restore")
	apiURL := fs.String("api-url", "", "management API HTTPS origin")
	keyStdin := fs.Bool("key-stdin", false, "read login API key from stdin")
	noBrowser := fs.Bool("no-browser", false, "show browser authorization URL without opening it")
	name := fs.String("name", "", "application or key name")
	permissions := fs.String("permissions", "deployments:write,deployments:read,logs:read", "comma-separated key permissions")
	ttl := fs.Duration("ttl", 24*time.Hour, "API key lifetime, maximum 90 days")
	tail := fs.Int64("tail", 100, "maximum log tail lines (1–1000)")
	follow := fs.Bool("follow", false, "follow live logs (each connection capped at ten minutes)")
	pod := fs.String("pod", "", "pod name for terminal or log search")
	container := fs.String("container", "app", "container name")
	query := fs.String("query", "", "SQL-like log predicate; returns sampled entries and histogram as JSON")
	since := fs.Duration("since", time.Hour, "log search window, maximum 24h")
	previous := fs.Bool("previous", false, "search the previous container instance")
	hostname := fs.String("hostname", "", "certificate DNS hostname")
	certificateFile := fs.String("certificate-file", "", "PEM certificate chain file, maximum 256 KiB")
	privateKeyFile := fs.String("private-key-file", "", "PEM private key file, maximum 32 KiB")
	fromIngress := fs.Bool("from-ingress", false, "snapshot this service's existing HTTP ingress certificate")
	commandJSON := fs.String("command-json", "", "terminal executable and arguments as a JSON array, defaults to /bin/sh")
	// Standard flags accept options before an identifier. Move ordinary positionals
	// to the end so `status APP --json` and `--json APP` behave consistently.
	if err := fs.Parse(reorder(os.Args[2:])); err != nil {
		return &exitError{2, err.Error()}
	}
	cfg, path, err := readConfig()
	if err != nil {
		return err
	}
	if value := os.Getenv("HAKOPOD_API_URL"); value != "" {
		cfg.URL = value
	}
	if value := os.Getenv("HAKOPOD_API_KEY"); value != "" {
		cfg.Key = value
	}
	if *apiURL != "" {
		cfg.URL = *apiURL
	}
	if *project != "" {
		cfg.Project = *project
	}
	if *environment != "" {
		cfg.Environment = *environment
	}
	if command == "init" {
		if *name == "" {
			*name = filepath.Base(mustWD())
		}
		data := fmt.Sprintf("schema_version = 1\nname = %q\n\n[services.web]\nimage = \"nginxinc/nginx-unprivileged:stable-alpine\"\nport = 8080\npublic = true\n", *name)
		if _, err := spec.Parse([]byte(data)); err != nil {
			return err
		}
		f, err := os.OpenFile(*file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err = f.WriteString(data); err != nil {
			return err
		}
		fmt.Printf("Created %s. Context: %s/%s (select with login or --project/--environment).\n", *file, cfg.Project, cfg.Environment)
		return nil
	}
	if command == "validate" {
		data, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		app, err := spec.Parse(data)
		if err != nil {
			return &exitError{2, err.Error()}
		}
		if *outputJSON {
			return printJSON(app)
		}
		fmt.Printf("Valid: %s, %d service(s), schema v%d\n", app.Name, len(app.Services), app.SchemaVersion)
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if command == "login" {
		if *keyStdin {
			raw, err := io.ReadAll(io.LimitReader(os.Stdin, 256))
			if err != nil {
				return err
			}
			cfg.Key = strings.TrimSpace(string(raw))
		} else {
			if cfg.Project == "" || cfg.Environment == "" {
				return &exitError{2, "browser login requires --project and --environment for the CLI session scope"}
			}
			session, err := deviceLogin(ctx, cfg, *noBrowser, strings.Split(*permissions, ","))
			if err != nil {
				return err
			}
			cfg.Key = session.Token
		}
		c, err := newClient(cfg)
		if err != nil {
			return err
		}
		var me store.Principal
		if err = c.request(ctx, "GET", "/me", nil, "", &me); err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		data, _ := json.MarshalIndent(cfg, "", "  ")
		tmp, err := os.CreateTemp(filepath.Dir(path), ".hakopod-credentials-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		if err = tmp.Chmod(0600); err == nil {
			_, err = tmp.Write(data)
		}
		if err == nil {
			err = tmp.Sync()
		}
		tmp.Close()
		if err == nil {
			err = os.Rename(tmp.Name(), path)
		}
		if err != nil {
			return err
		}
		fmt.Printf("Logged in as %s. Credentials stored in restricted file %s (0600).\n", me.Name, path)
		return nil
	}
	c, err := newClient(cfg)
	if err != nil {
		return err
	}
	if command == "logout" {
		if strings.HasPrefix(cfg.Key, "hs_") {
			if err = c.request(ctx, "POST", "/auth/logout", map[string]any{}, "", nil); err != nil {
				return err
			}
		}
		if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Println("Logged out; saved credentials removed.")
		return nil
	}
	arg := ""
	if fs.NArg() > 0 {
		arg = fs.Arg(0)
	}
	switch command {
	case "projects", "nodes", "keys", "audit":
		var out any
		if err = c.request(ctx, "GET", "/"+command, nil, "", &out); err != nil {
			return err
		}
		return printJSON(out)
	case "project-create":
		if *name == "" || cfg.Environment == "" {
			return &exitError{2, "project-create requires --name and --environment"}
		}
		var out any
		if err = c.request(ctx, "POST", "/projects", map[string]string{"name": *name, "environment": cfg.Environment}, "", &out); err != nil {
			return err
		}
		return printJSON(out)
	case "key-create":
		if *name == "" {
			return &exitError{2, "key-create requires --name"}
		}
		var out any
		if err = c.request(ctx, "POST", "/keys", store.KeyInput{Name: *name, Project: cfg.Project, Environment: cfg.Environment, Permissions: strings.Split(*permissions, ","), ExpiresAt: time.Now().Add(*ttl).UTC()}, "", &out); err != nil {
			return err
		}
		return printJSON(out)
	case "key-revoke":
		if arg == "" {
			return &exitError{2, "key-revoke requires a key ID"}
		}
		var out any
		if err = c.request(ctx, "DELETE", "/keys/"+url.PathEscape(arg), nil, "", &out); err != nil {
			return err
		}
		return printJSON(out)
	case "cancel":
		if arg == "" {
			return &exitError{2, "cancel requires a deployment ID"}
		}
		var out any
		if err = c.request(ctx, "POST", "/deployments/"+url.PathEscape(arg)+"/cancel", map[string]any{}, "", &out); err != nil {
			return err
		}
		return printJSON(out)
	}
	if cfg.Project == "" || cfg.Environment == "" {
		return &exitError{2, "project and environment are required; use --project/--environment or a saved login context"}
	}
	switch command {
	case "plan", "deploy":
		data, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		parsedSpec, err := spec.Parse(data)
		if err != nil {
			return &exitError{2, err.Error()}
		}
		if command == "deploy" && *idem != "" {
			var previous store.Deployment
			lookupErr := c.request(ctx, "GET", "/idempotency/"+url.PathEscape(*idem), nil, "", &previous)
			if lookupErr == nil {
				var app store.Application
				if err = c.request(ctx, "GET", "/applications/"+previous.ApplicationID, nil, "", &app); err != nil {
					return err
				}
				same := bytes.Equal(store.JSON(parsedSpec), store.JSON(previous.Spec))
				if *service != "" {
					requested, exists := parsedSpec.Services[*service]
					saved, wasPresent := previous.Spec.Services[*service]
					same = exists && wasPresent && parsedSpec.Name == previous.Spec.Name && bytes.Equal(store.JSON(requested), store.JSON(saved))
				}
				if !same || app.Project != cfg.Project || app.Environment != cfg.Environment {
					return &exitError{4, "idempotency key already belongs to different deployment input"}
				}
				if *wait {
					previous, err = waitDeployment(ctx, c, previous)
					if err != nil {
						return err
					}
				}
				return deploymentOutput(previous, *outputJSON)
			}
			var apiErr *exitError
			if !errors.As(lookupErr, &apiErr) || !strings.HasPrefix(apiErr.message, "not_found:") {
				return lookupErr
			}
		}
		in := map[string]any{"project": cfg.Project, "environment": cfg.Environment, "toml": string(data)}
		if *service != "" {
			in["service"] = *service
		}
		var plan struct {
			ApplicationID    string           `json:"application_id"`
			ExpectedRevision int64            `json:"expected_revision"`
			Spec             spec.Application `json:"spec"`
			Changes          []spec.Change    `json:"changes"`
			Warnings         []string         `json:"warnings"`
			ResourceProfiles any              `json:"resource_profiles"`
		}
		if err = c.request(ctx, "POST", "/plan", in, "", &plan); err != nil {
			return err
		}
		if command == "plan" {
			return printJSON(plan)
		}
		// Submit the canonical reviewed full revision so retries do not merge a target
		// into a later application state under a reused idempotency key.
		in = map[string]any{"project": cfg.Project, "environment": cfg.Environment, "spec": plan.Spec, "expected_revision": plan.ExpectedRevision}
		if *idem == "" {
			*idem = store.NewID()
		}
		if !*outputJSON {
			fmt.Fprintf(os.Stderr, "Submitting %s revision %d (%d changes); retry key %s\n", plan.Spec.Name, plan.ExpectedRevision+1, len(plan.Changes), *idem)
		}
		var d store.Deployment
		if err = c.request(ctx, "POST", "/deployments", in, *idem, &d); err != nil {
			return err
		}
		if *wait {
			d, err = waitDeployment(ctx, c, d)
			if err != nil {
				return err
			}
		}
		return deploymentOutput(d, *outputJSON)
	case "status", "logs", "rollback", "services", "networks", "terminal", "certificates", "certificate-upload", "delivery":
		a, err := findApp(ctx, c, cfg, arg, *file)
		if err != nil {
			return err
		}
		switch command {
		case "certificates":
			out, err := serviceCertificates(ctx, c, a, *service)
			if err != nil {
				return err
			}
			return printJSON(out)
		case "certificate-upload":
			if _, err := serviceDeliveryPath(a, *service); err != nil {
				return err
			}
			input, err := readCertificateInput(*hostname, *certificateFile, *privateKeyFile, *fromIngress)
			if err != nil {
				return err
			}
			out, err := uploadServiceCertificate(ctx, c, a, *service, input)
			if err != nil {
				return err
			}
			return printJSON(out)
		case "delivery":
			out, err := serviceDelivery(ctx, c, a, *service)
			if err != nil {
				return err
			}
			return printJSON(out)
		case "terminal":
			return terminal(ctx, c, a.ID, *service, *pod, *container, *commandJSON)
		case "status":
			if *outputJSON {
				return printJSON(a)
			}
			fmt.Printf("%s  %s/%s  revision %d  %s\n", a.Name, a.Project, a.Environment, a.Revision, a.Status)
			fmt.Println(string(a.Observed))
			return nil
		case "services":
			return printJSON(a.Spec.Services)
		case "networks":
			return printJSON(a.Spec.Networks)
		case "logs":
			if *service == "" {
				if len(a.Spec.Services) == 1 {
					for n := range a.Spec.Services {
						*service = n
					}
				} else {
					return &exitError{2, "--service is required for a multi-service application"}
				}
			}
			if *query != "" || *pod != "" || *previous || *container != "app" {
				if *follow {
					return &exitError{2, "log search is a bounded snapshot; omit --follow"}
				}
				var result any
				if err := c.request(ctx, "POST", "/applications/"+a.ID+"/logs/query", map[string]any{"service": *service, "pod": *pod, "container": *container, "query": *query, "tail": *tail, "since_seconds": int64(since.Seconds()), "previous": *previous}, "", &result); err != nil {
					return err
				}
				return printJSON(result)
			}
			path := "/applications/" + a.ID + "/logs?" + url.Values{"service": {*service}, "tail": {fmt.Sprint(*tail)}, "follow": {fmt.Sprint(*follow)}}.Encode()
			req, _ := http.NewRequestWithContext(ctx, "GET", c.url+"/api/v1"+path, nil)
			req.Header.Set("Authorization", "Bearer "+c.key)
			streamClient := *c.http
			streamClient.Timeout = 11 * time.Minute
			res, err := streamClient.Do(req)
			if err != nil {
				return errors.New("log connection failed; verify API connectivity")
			}
			defer res.Body.Close()
			if res.StatusCode != 200 {
				return responseError(res)
			}
			_, err = io.CopyBuffer(os.Stdout, res.Body, make([]byte, 16<<10))
			return err
		case "rollback":
			if *revision < 1 {
				return &exitError{2, "--revision must name a successful deployment"}
			}
			if *idem == "" {
				*idem = store.NewID()
			}
			var d store.Deployment
			if err = c.request(ctx, "POST", "/applications/"+a.ID+"/rollback", map[string]any{"revision": *revision, "expected_revision": a.Revision}, *idem, &d); err != nil {
				return err
			}
			if *wait {
				d, err = waitDeployment(ctx, c, d)
				if err != nil {
					return err
				}
			}
			return deploymentOutput(d, *outputJSON)
		}
	default:
		return &exitError{2, "unknown command " + command + "; run hakopod help"}
	}
	return nil
}
func newClient(cfg config) (*client, error) {
	if cfg.URL == "" || cfg.Key == "" {
		return nil, &exitError{2, "HAKOPOD_API_URL and HAKOPOD_API_KEY (or login credentials) are required; noninteractive commands never prompt"}
	}
	return anonymousClient(cfg)
}
func anonymousClient(cfg config) (*client, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, &exitError{2, "API URL must be an HTTPS origin, without credentials, path or query"}
	}
	loopback := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil && ip.IsLoopback() {
		loopback = true
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, &exitError{2, "API keys require verified HTTPS; HTTP is allowed only on local loopback for development"}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 4
	transport.MaxIdleConnsPerHost = 2
	transport.ResponseHeaderTimeout = 20 * time.Second
	return &client{url: strings.TrimRight(cfg.URL, "/"), key: cfg.Key, http: &http.Client{Timeout: 30 * time.Second, Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error { return errors.New("API redirects are not allowed") }}}, nil
}
func (c *client) request(ctx context.Context, method, path string, in any, idem string, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url+"/api/v1"+path, body)
	if err != nil {
		return err
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	req.Header.Set("Content-Type", "application/json")
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return errors.New("API request failed; verify URL/connectivity (accepted work survives client disconnection)")
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return responseError(res)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out)
}
func responseError(res *http.Response) error {
	var body struct {
		Error struct{ Code, Message string }
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 32<<10)).Decode(&body); err != nil {
		return fmt.Errorf("API returned HTTP %d", res.StatusCode)
	}
	code := 1
	if res.StatusCode == 400 {
		code = 2
	}
	if res.StatusCode == 401 || res.StatusCode == 403 {
		code = 3
	}
	if res.StatusCode == 409 {
		code = 4
	}
	return &exitError{code, body.Error.Code + ": " + body.Error.Message}
}
func waitDeployment(ctx context.Context, c *client, d store.Deployment) (store.Deployment, error) {
	timer := time.NewTicker(2 * time.Second)
	defer timer.Stop()
	for d.Status == "queued" || d.Status == "running" {
		select {
		case <-ctx.Done():
			return d, errors.New("stopped waiting; accepted deployment continues on the server")
		case <-timer.C:
			if err := c.request(ctx, "GET", "/deployments/"+d.ID, nil, "", &d); err != nil {
				return d, err
			}
		}
	}
	return d, nil
}
func deploymentOutput(d store.Deployment, jsonOut bool) error {
	if jsonOut {
		if err := printJSON(d); err != nil {
			return err
		}
	} else {
		fmt.Printf("%s  revision %d  %s\n", d.ID, d.Revision, d.Status)
		if d.Error != "" {
			fmt.Println(d.Error)
		}
	}
	if d.Status == "failed" || d.Status == "cancelled" || d.Status == "superseded" {
		return &exitError{5, "deployment did not succeed: " + d.Status}
	}
	return nil
}
func findApp(ctx context.Context, c *client, cfg config, name, file string) (store.Application, error) {
	if name == "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return store.Application{}, err
		}
		a, err := spec.Parse(data)
		if err != nil {
			return store.Application{}, err
		}
		name = a.Name
	}
	var list struct {
		Items      []store.Application `json:"items"`
		NextCursor string              `json:"next_cursor"`
	}
	cursor := ""
	for {
		path := "/applications?" + url.Values{"project": {cfg.Project}, "environment": {cfg.Environment}, "cursor": {cursor}}.Encode()
		if err := c.request(ctx, "GET", path, nil, "", &list); err != nil {
			return store.Application{}, err
		}
		for _, a := range list.Items {
			if a.Name == name || a.ID == name {
				var full store.Application
				err := c.request(ctx, "GET", "/applications/"+a.ID, nil, "", &full)
				return full, err
			}
		}
		if list.NextCursor == "" || list.NextCursor == cursor {
			break
		}
		cursor = list.NextCursor
	}
	return store.Application{}, fmt.Errorf("application %q not found in %s/%s", name, cfg.Project, cfg.Environment)
}
func readConfig() (config, string, error) {
	path := os.Getenv("HAKOPOD_CONFIG")
	if path == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return config{}, "", err
		}
		path = filepath.Join(base, "hakopod", "config.json")
	}
	var c config
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, path, nil
	}
	if err != nil {
		return c, path, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return c, path, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return c, path, fmt.Errorf("credential file %s must have mode 0600", path)
	}
	err = json.Unmarshal(data, &c)
	return c, path, err
}
func printJSON(v any) error {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	return e.Encode(v)
}
func mustWD() string { p, _ := os.Getwd(); return p }
func reorder(args []string) []string {
	flags, pos := []string{}, []string{}
	bools := map[string]bool{"--json": true, "--wait": true, "--key-stdin": true, "--no-browser": true, "--follow": true, "--previous": true, "--from-ingress": true, "--help": true, "-h": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && !bools[a] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}
func help() {
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	fmt.Fprintln(w, `Hakopod — deploy containers on infrastructure you own.

  hakopod login --api-url https://control.example.com --project demo --environment development
  hakopod login --api-url https://control.example.com --key-stdin  # CI machine key
  hakopod logout
  hakopod init --name shop
  hakopod validate
  hakopod plan --project demo --environment development
  hakopod deploy --project demo --environment development --wait
  hakopod status shop --project demo --environment development
  hakopod logs shop --service api --follow
  hakopod logs shop --service api --query "severity >= ERROR" --since 1h
  hakopod terminal shop --service api --pod POD_NAME
  hakopod terminal shop --service db --pod POD_NAME --command-json '["psql", "-U", "postgres"]'
  hakopod certificates mail --service smtp
  hakopod certificate-upload mail --service smtp --hostname mail.example.com --certificate-file fullchain.pem --private-key-file privkey.pem
  hakopod certificate-upload mail --service smtp --hostname mail.example.com --from-ingress
  hakopod delivery mail --service smtp
  hakopod rollback shop --revision 1 --wait
  hakopod services|networks [APPLICATION]
  hakopod projects|nodes|keys|audit
  hakopod project-create --name demo --environment production
  hakopod key-create --name ci --project demo --environment production --ttl 24h
  hakopod key-revoke KEY_ID
  hakopod cancel DEPLOYMENT_ID

CI: set HAKOPOD_API_URL and HAKOPOD_API_KEY in the CI secret store.
Common flags: --file, --project, --environment, --service, --json.
Exit codes: 0 success/accepted, 1 transport/server, 2 input, 3 forbidden,
4 revision conflict, 5 failed/cancelled release. --wait exits only at terminal state.
Logs are live and ephemeral; disconnecting does not cancel deployments.`)
}

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

// Release builds inject the tag version with -X main.version.
var version = "0.1.0-dev"

const (
	defaultResponseHeaderTimeout = 80 * time.Second
	defaultRequestTimeout        = 90 * time.Second
)

type config struct {
	Workspace   string `json:"workspace,omitempty"`
	URL         string `json:"url"`
	Key         string `json:"key"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
}
type client struct {
	url, key, workspace string
	http                *http.Client
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
	workspace := fs.String("workspace", "", "Cloud workspace ID; does not grant access")
	environment := fs.String("environment", "", "environment context")
	service := fs.String("service", "", "service name")
	branch := fs.String("branch", "", "branch/reference label for a preview image")
	discardPreview := fs.Bool("acknowledge-data-expiry", false, "allow automatic deletion of preview workloads, volumes and native secrets")
	allowDeploy := fs.Bool("allow-deploy", false, "enable reviewed MCP deployment tool")
	allowWrite := fs.Bool("allow-write", false, "enable contract operation mutations")
	allowExec := fs.Bool("allow-exec", false, "enable bounded service pod execution")
	allowSQL := fs.Bool("allow-sql", false, "enable bounded database SQL queries")
	allowSQLWrite := fs.Bool("allow-sql-write", false, "enable database SQL writes with --allow-sql")
	apiPathJSON := fs.String("path-json", "", "operation path parameters as a JSON object")
	apiQueryJSON := fs.String("query-json", "", "operation query parameters as a JSON object")
	apiCursor := fs.String("cursor", "", "operation discovery cursor")
	apiFamily := fs.String("family", "", "operation discovery resource family")
	apiOperation := fs.String("operation", "", "operation schema to inspect")
	apiLimit := fs.Int("limit", 25, "operation discovery page size, 1 to 100")
	sqlFile := fs.String("sql-file", "", "SQL statement file")
	sqlParameters := fs.String("parameters-json", "", "SQL parameter JSON array")
	sqlMaxRows := fs.Int("max-rows", 100, "SQL row cap, at most 1000")
	sqlMaxBytes := fs.Int("max-bytes", 256<<10, "SQL response byte cap, at most 1 MiB")
	execTimeout := fs.Int("timeout-seconds", 20, "pod execution timeout, 1 to 20 seconds")
	execMaxOutput := fs.Int("max-output-bytes", 65536, "pod output cap per stream, at most 65536")
	apiBodyFile := fs.String("body-file", "", "operation JSON request body file")
	outputJSON := fs.Bool("json", false, "machine-readable JSON")
	wait := fs.Bool("wait", false, "wait for final deployment outcome")
	idem := fs.String("idempotency-key", "", "stable retry key (generated if omitted)")
	revision := fs.Int64("revision", 0, "successful revision to restore")
	reviewID := fs.String("review-id", "", "accepted database resize, recovery, connection or switchover review")
	databaseTargetMember := fs.String("target-member", "", "physical standby member to review for Oracle switchover")
	databaseOperation := fs.String("operation-id", "", "failed replica change or Oracle switchover operation to retry")
	databasePublicEndpointID := fs.String("public-endpoint-id", "", "database public endpoint ID to revoke")
	databasePublicEndpointPurpose := fs.String("purpose", "", "public database route purpose from database public-endpoint-capabilities")
	databasePublicEndpointCIDRs := fs.String("source-cidrs", "", "comma-separated IPv4 CIDRs allowed to connect")
	databasePublicEndpointRevision := fs.Int64("endpoint-revision", -1, "reviewed database public endpoint revision")
	databasePublicEndpointMaxConnections := fs.Int64("max-connections", 32, "public database endpoint connection cap, 1–256")
	databaseApplication := fs.String("application-id", "", "application ID for a managed database connection")
	databaseVariable := fs.String("variable", "DATABASE_URL", "managed database connection environment variable")
	databaseEndpoint := fs.String("endpoint", "read_write", "managed database endpoint: read_write, read_only or cluster")
	databaseUsername := fs.String("username", "", "existing managed database login; omit for the managed default")
	databaseName := fs.String("database", "", "existing database name or Redis database number; omit for the managed default")
	databasePasswordSecret := fs.String("password-secret", "", "native secret containing the database password in the application's scope")
	databaseSSLMode := fs.String("ssl-mode", "", "binding SSL mode supported by the database; omit to follow its TLS policy")
	databaseHistoryRange := fs.String("range", "1h", "database monitoring range: 1h, 6h or 24h")
	databaseClusterAware := fs.Bool("cluster-aware", false, "acknowledge that the application uses a cluster-aware Redis client")
	databaseRecoveryJob := fs.String("job-id", "", "completed database recovery job to inspect")
	databaseInspected := fs.Bool("inspected", false, "attest that the recovered data was inspected")
	externalCredentialsFile := fs.String("credentials-file", "", "legacy external database credentials JSON file with restricted permissions")
	externalCredentialsStdin := fs.Bool("credentials-stdin", false, "read legacy external database credentials JSON from redirected stdin")
	importDestination := fs.String("destination-id", "", "archive import S3 destination")
	importEngine := fs.String("engine", "postgresql", "archive import engine: postgresql or redis")
	importVersion := fs.String("source-version", "", "archive import source major version")
	importCaptured := fs.String("captured-at", "", "archive recovery point as RFC3339, including timezone")
	artifactID := fs.String("artifact-id", "", "verified database backup archive")
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
	dir := fs.String("dir", "", "folder tree: optional network.toml plus one application per subfolder")
	only := fs.String("only", "", "with --dir, comma-separated folder or application names to include")
	noNetwork := fs.Bool("no-network", false, "with --dir, validate network.toml locally but do not plan or apply it")
	// Standard flags accept options before an identifier. Move ordinary positionals
	// to the end so `status APP --json` and `--json APP` behave consistently.
	if err := fs.Parse(reorder(os.Args[2:])); err != nil {
		return &exitError{2, err.Error()}
	}
	if command == "api" && fs.NArg() == 1 && fs.Arg(0) == "operations" {
		return runAPIOperation(context.Background(), nil, config{}, fs.Args(), "", "", "", "", false, *apiCursor, *apiFamily, *apiOperation, *apiLimit)
	}
	if command == "external-database" && *wait {
		return &exitError{2, "use external-database operation OPERATION_ID to follow the accepted operation"}
	}
	if command != "external-database" && (*externalCredentialsFile != "" || *externalCredentialsStdin) {
		return &exitError{2, "--credentials-file and --credentials-stdin apply only to external-database rotate"}
	}
	if command != "database" && (*databaseTargetMember != "" || *databaseOperation != "") {
		return &exitError{2, "--target-member and --operation-id apply only to database"}
	}
	if command == "database" && *wait {
		return &exitError{2, "use database operation OPERATION_ID to follow the accepted operation"}
	}
	explicitFile := false
	fs.Visit(func(f *flag.Flag) { explicitFile = explicitFile || f.Name == "file" })
	if !explicitFile {
		// Linux filesystems are case-sensitive; accept HAKOPOD.toml and friends as the default.
		if _, err := os.Stat(*file); os.IsNotExist(err) {
			found, err := findConfigFile(".")
			if err != nil {
				return &exitError{2, err.Error()}
			}
			if found != "" {
				*file = found
			}
		}
	}
	if *dir != "" {
		if command != "validate" && command != "plan" && command != "deploy" {
			return &exitError{2, "--dir applies only to validate, plan and deploy"}
		}
		if *service != "" || *idem != "" {
			return &exitError{2, "--service and --idempotency-key cannot be combined with --dir; each application gets its own retry key"}
		}
		if explicitFile {
			return &exitError{2, "--file cannot be combined with --dir; each folder's hakopod.toml is used"}
		}
	} else if *only != "" || *noNetwork {
		return &exitError{2, "--only and --no-network require --dir"}
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
	if value := os.Getenv("HAKOPOD_WORKSPACE"); value != "" {
		cfg.Workspace = value
	}
	if *workspace != "" {
		cfg.Workspace = *workspace
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
	if command == "validate" && *dir != "" {
		return validateTree(*dir, splitList(*only), *outputJSON)
	}
	if command == "validate" {
		data, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		app, envFiles, err := validateLocalConfiguration(*file, data)
		if err != nil {
			return &exitError{2, err.Error()}
		}
		if *outputJSON {
			if len(envFiles) > 0 {
				return printJSON(map[string]any{"valid": true, "name": app.Name, "service_count": len(app.Services), "env_file_count": len(envFiles), "message": "Run plan to upload files and create scoped secret references"})
			}
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
	case "external-database":
		return externalDatabaseCommand(ctx, c, cfg.Project, cfg.Environment, fs.Args(), externalDatabaseFlags{CredentialsFile: *externalCredentialsFile, CredentialsStdin: *externalCredentialsStdin, IdempotencyKey: *idem, ReviewID: *reviewID, Name: *name, Revision: *revision, ApplicationID: *databaseApplication, Service: *service, Variable: *databaseVariable})
	case "exec":
		if fs.NArg() != 1 {
			return errors.New("usage: hakopod exec APP --service SERVICE --pod POD --container CONTAINER --command-json ARRAY --allow-exec")
		}
		return runExecution(ctx, c, cfg, "exec", fs.Arg(0), executionFlags{Service: *service, Pod: *pod, Container: *container, CommandJSON: *commandJSON, AllowExec: *allowExec, Timeout: *execTimeout, MaxOutput: *execMaxOutput})
	case "database":
		if fs.Arg(0) == "query" {
			if fs.NArg() != 2 {
				return errors.New("usage: hakopod database query ID --sql-file FILE [--parameters-json ARRAY] [--allow-sql-write --revision REVISION]")
			}
			return runExecution(ctx, c, cfg, "query", fs.Arg(1), executionFlags{SQLFile: *sqlFile, ParametersJSON: *sqlParameters, AllowSQLWrite: *allowSQLWrite, MaxRows: *sqlMaxRows, MaxBytes: *sqlMaxBytes, ExpectedRevision: *revision})
		}
		if !explicitFile && (fs.Arg(0) == "resize-retry-plan" || fs.Arg(0) == "resize-retry") {
			// A retry uses its saved specification, not the default application file.
			*file = ""
		}
		return databaseCommand(ctx, c, cfg.Project, cfg.Environment, fs.Args(), *file, *idem, *reviewID, *artifactID, *name, *revision, databaseConnectionFlags{ApplicationID: *databaseApplication, Service: *service, Variable: *databaseVariable, Endpoint: *databaseEndpoint, Username: *databaseUsername, Database: *databaseName, PasswordSecret: *databasePasswordSecret, SSLMode: *databaseSSLMode, HistoryRange: *databaseHistoryRange, JobID: *databaseRecoveryJob, TargetMember: *databaseTargetMember, OperationID: *databaseOperation, PublicEndpointID: *databasePublicEndpointID, PublicEndpointPurpose: *databasePublicEndpointPurpose, PublicEndpointCIDRs: *databasePublicEndpointCIDRs, PublicEndpointRevision: *databasePublicEndpointRevision, PublicEndpointMaxConnections: *databasePublicEndpointMaxConnections, ClusterAware: *databaseClusterAware, Inspected: *databaseInspected, Import: databaseImportFlags{*importDestination, *importEngine, *importVersion, *importCaptured}})
	case "platform":
		return managedPlatformCommand(ctx, c, cfg.Project, cfg.Environment, fs.Args(), *file, *idem, *name)
	case "previews", "preview-create", "preview-delete":
		return previewCommand(ctx, c, cfg, command, arg, *name, *branch, *file, *idem, *ttl, *discardPreview)
	case "api":
		return runAPIOperation(ctx, c, cfg, fs.Args(), *apiPathJSON, *apiQueryJSON, *apiBodyFile, *idem, *allowWrite, *apiCursor, *apiFamily, *apiOperation, *apiLimit)
	case "mcp":
		return serveAgentOptions(ctx, c, cfg, *allowDeploy, *allowWrite, *allowExec, *allowSQL, *allowSQLWrite, os.Stdin, os.Stdout)
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
		if *dir != "" {
			return treeCommand(ctx, c, cfg, command, *dir, splitList(*only), *noNetwork, *wait, *outputJSON)
		}
		data, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		parsedSpec, envFiles, err := validateLocalConfiguration(*file, data)
		if err != nil {
			return &exitError{2, err.Error()}
		}
		var plan appPlan
		if len(envFiles) > 0 {
			if plan, err = planApplication(ctx, c, cfg, data, envFiles, *service); err != nil {
				return err
			}
			parsedSpec = plan.Spec
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
		if len(envFiles) == 0 {
			if plan, err = planApplication(ctx, c, cfg, data, nil, *service); err != nil {
				return err
			}
		}
		if command == "plan" {
			return printJSON(plan)
		}
		if err = setupDeploymentSecrets(ctx, c, cfg.Project, cfg.Environment, plan.Spec, plan.MissingSecrets, *outputJSON); err != nil {
			return err
		}
		if *idem == "" {
			*idem = store.NewID()
		}
		d, err := submitDeployment(ctx, c, cfg, plan, *idem, *outputJSON)
		if err != nil {
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
			c.authorizeRequest(req)
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
	if cfg.Workspace != "" && (len(cfg.Workspace) != 32 || strings.Trim(cfg.Workspace, "0123456789abcdef") != "") {
		return nil, &exitError{2, "workspace must be a 32-character Cloud workspace ID"}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 4
	transport.MaxIdleConnsPerHost = 2
	transport.ResponseHeaderTimeout = defaultResponseHeaderTimeout
	return &client{url: strings.TrimRight(cfg.URL, "/"), key: cfg.Key, workspace: cfg.Workspace, http: &http.Client{Timeout: defaultRequestTimeout, Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error { return errors.New("API redirects are not allowed") }}}, nil
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
	c.authorizeRequest(req)
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
	decoder := json.NewDecoder(io.LimitReader(res.Body, 8<<20))
	decoder.UseNumber()
	return decoder.Decode(out)
}
func responseError(res *http.Response) error {
	var body struct {
		Outcome     string `json:"outcome"`
		OperationID string `json:"operation_id"`
		Error       struct {
			Code, Message string
			Outcome       string `json:"outcome"`
			OperationID   string `json:"operation_id"`
		}
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
	if body.Outcome != "" {
		body.Error.Outcome = body.Outcome
	}
	if body.OperationID != "" {
		body.Error.OperationID = body.OperationID
	}
	message := body.Error.Code + ": " + body.Error.Message
	if body.Error.Outcome == "unknown" || body.Error.Outcome == "not_started" || body.Error.Outcome == "rolled_back" {
		message += " (outcome=" + body.Error.Outcome + ")"
	}
	if agentID.MatchString(body.Error.OperationID) {
		message += " (operation_id=" + body.Error.OperationID + ")"
	}
	return &exitError{code, message}
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
		a, _, err := validateLocalConfiguration(file, data)
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
	if err := checkConfigPerm(info); err != nil {
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
	bools := map[string]bool{"--credentials-stdin": true, "--disconnect": true, "--acknowledge-data-expiry": true, "--allow-deploy": true, "--allow-write": true, "--allow-exec": true, "--allow-sql": true, "--allow-sql-write": true, "--json": true, "--wait": true, "--key-stdin": true, "--no-browser": true, "--follow": true, "--previous": true, "--from-ingress": true, "--no-network": true, "--help": true, "-h": true}
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
	fmt.Println("  exec APP --service SERVICE --pod POD --container CONTAINER --command-json ARRAY --allow-exec")
	fmt.Println("  database query ID --sql-file FILE [--parameters-json ARRAY] [--allow-sql-write --revision REVISION]")
	fmt.Println("  api operations | api call OPERATION [--path-json JSON] [--query-json JSON] [--body-file FILE] [--allow-write]")
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
  hakopod validate --dir devops
  hakopod plan --dir devops --project demo --environment development
  hakopod deploy --dir devops --project demo --environment development --wait [--only syne,lumen]
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
  hakopod database list --project demo --environment development
  hakopod platform list --project demo --environment development
  hakopod platform catalog --project demo --environment development
  hakopod platform trust PLATFORM_ID
  hakopod platform review|apply --file supabase.toml --project demo --environment development
  hakopod platform show|operations PLATFORM_ID
  hakopod platform update PLATFORM_ID --file supabase.toml
  hakopod platform delete PLATFORM_ID --name PLATFORM_NAME
  hakopod platform operation OPERATION_ID
  hakopod platform recovery-review --file recovery.toml
  hakopod platform recovery-apply --file recovery.toml --idempotency-key RETRY_KEY
  hakopod platform recovery-operations PLATFORM_ID
  hakopod platform recovery-operation OPERATION_ID
  hakopod platform recovery-cancel OPERATION_ID
  hakopod database nodes --project demo --environment development
  hakopod database create --file database.toml --project demo --environment development
  hakopod database show DATABASE_ID
  hakopod database resize-plan DATABASE_ID --file database.toml
  hakopod database resize DATABASE_ID --file database.toml --review-id REVIEW_ID --revision 1
  hakopod database resize-retry-plan DATABASE_ID --operation-id OPERATION_ID --revision 2
  hakopod database resize-retry DATABASE_ID --operation-id OPERATION_ID --review-id REVIEW_ID --revision 2 --name DATABASE_NAME --idempotency-key RETRY_KEY
  hakopod database switchover-plan DATABASE_ID --target-member STANDBY_MEMBER
  hakopod database switchover DATABASE_ID --review-id REVIEW_ID --revision 1 --name DATABASE_NAME --idempotency-key RETRY_KEY
  hakopod database switchover-retry DATABASE_ID --operation-id OPERATION_ID --revision 1 --name DATABASE_NAME
  hakopod database operation OPERATION_ID
  hakopod database public-endpoint-capabilities DATABASE_ID
  hakopod database public-endpoint-list DATABASE_ID
  hakopod database public-endpoint-plan DATABASE_ID --purpose read_write --source-cidrs 192.0.2.0/24 --max-connections 32
  hakopod database public-endpoint-publish DATABASE_ID --review-id REVIEW_ID --revision 1 --endpoint-revision 0 --idempotency-key RETRY_KEY
  hakopod database public-endpoint-revoke DATABASE_ID --public-endpoint-id ENDPOINT_ID --endpoint-revision 1 --idempotency-key RETRY_KEY
  hakopod database public-endpoint-operation OPERATION_ID
  hakopod database connection-plan DATABASE_ID --application-id APP_ID --service api --variable DATABASE_URL --endpoint read_write --username app_user --database app_db --password-secret db-password --ssl-mode verify-full
  hakopod database connect DATABASE_ID --review-id REVIEW_ID --name APP_NAME
  hakopod database inspect DATABASE_ID --job-id JOB_ID --revision 1 --name DATABASE_NAME --inspected
  hakopod database import-plan --file DUMP --destination-id DESTINATION_ID --name docker-source --engine postgresql --source-version 17 --captured-at RFC3339
  hakopod database import IMPORT_ID --file DUMP --name docker-source
  hakopod database restore-plan DATABASE_ID --artifact-id ARTIFACT_ID
  hakopod database restore DATABASE_ID --artifact-id ARTIFACT_ID --review-id REVIEW_ID --name TARGET_NAME
  hakopod external-database list --project demo --environment development
  hakopod external-database show|trust|connections CONNECTION_ID
  hakopod external-database operation OPERATION_ID
  hakopod external-database rotate CONNECTION_ID --credentials-file credentials.json --revision 1 --name CONNECTION_NAME
  hakopod external-database refresh-review CONNECTION_ID --application-id APP_ID --service api --variable DATABASE_URL
  hakopod external-database refresh CONNECTION_ID --review-id REVIEW_ID --name APP_NAME
  hakopod external-database disconnect-review CONNECTION_ID --application-id APP_ID --service api --variable DATABASE_URL
  hakopod external-database disconnect CONNECTION_ID --review-id REVIEW_ID --name APP_NAME
  hakopod external-database delete CONNECTION_ID --revision 1 --name CONNECTION_NAME

External database credential rotation accepts JSON with username and password
through a restricted --credentials-file or redirected --credentials-stdin.

CI: set HAKOPOD_API_URL and HAKOPOD_API_KEY in the CI secret store.
Previews: hakopod preview-create APP --name pr-123 --file preview.toml --ttl 24h --acknowledge-data-expiry
          hakopod previews APP
          hakopod preview-delete ID --name pr-123

Agent integration: hakopod mcp --project PROJECT --environment ENV [--allow-deploy]

Common flags: --file, --project, --environment, --service, --json.
Folder trees: --dir ROOT reads an optional ROOT/network.toml and one hakopod.toml per
subfolder; --only a,b limits it to those folders or application names. The network is
applied first, then applications deploy in folder order, stopping at the first failure.
Managing virtual networks requires a project administrator; CI machine keys can pass
--no-network to validate network.toml locally and deploy only the applications.
Exit codes: 0 success/accepted, 1 transport/server, 2 input, 3 forbidden,
4 revision conflict, 5 failed/cancelled release. --wait exits only at terminal state.
Logs are live and ephemeral; disconnecting does not cancel deployments.`)
}

func (c *client) authorizeRequest(req *http.Request) {
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	if c.workspace != "" {
		req.Header.Set("X-Hakopod-Workspace", c.workspace)
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/externaldatabase"
	"github.com/hakopod/hakopod/internal/store"
	"golang.org/x/term"
)

type externalDatabaseFlags struct {
	File, CredentialsFile, IdempotencyKey, ReviewID, Name string
	ApplicationID, Service, Variable                      string
	Revision                                              int64
	CredentialsStdin, Disconnect                          bool
}

func readExternalDatabaseCredentials(path string, stdin bool, input *os.File) (*externaldatabase.Credentials, error) {
	if path != "" && stdin {
		return nil, &exitError{2, "choose --credentials-file or --credentials-stdin"}
	}
	if path == "" && !stdin {
		return nil, nil
	}
	var reader io.Reader
	if stdin {
		if input == nil || term.IsTerminal(int(input.Fd())) {
			return nil, &exitError{2, "--credentials-stdin requires redirected JSON input; interactive input is refused to prevent credential echo"}
		}
		reader = input
	} else {
		if runtime.GOOS == "windows" {
			return nil, &exitError{2, "use --credentials-stdin on Windows; credential-file ACL verification is unavailable"}
		}
		before, err := os.Lstat(path)
		if err != nil || !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > 16<<10 || checkConfigPerm(before) != nil {
			return nil, &exitError{2, "--credentials-file requires a nonempty regular file of at most 16 KiB with mode 0600 or tighter"}
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, &exitError{2, "credential file could not be opened"}
		}
		defer file.Close()
		after, err := file.Stat()
		if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || checkConfigPerm(after) != nil {
			return nil, &exitError{2, "credential file changed or has unsafe permissions"}
		}
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, (16<<10)+1))
	if err != nil || len(data) == 0 || len(data) > 16<<10 {
		return nil, &exitError{2, "credentials must be nonempty JSON of at most 16 KiB"}
	}
	defer clear(data)
	var credentials externaldatabase.Credentials
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&credentials) != nil || decoder.Decode(&struct{}{}) != io.EOF || credentials.Validate() != nil {
		return nil, &exitError{2, "credentials must contain only a valid username and password in one JSON object"}
	}
	return &credentials, nil
}

func readExternalDatabaseSpec(path string) (externaldatabase.Spec, error) {
	var zero externaldatabase.Spec
	if path == "" {
		return zero, &exitError{2, "provide --file with the external database TOML"}
	}
	before, err := os.Stat(path)
	if err != nil || !before.Mode().IsRegular() {
		return zero, &exitError{2, "external database TOML must be a regular file"}
	}
	file, err := os.Open(path)
	if err != nil {
		return zero, &exitError{2, "external database TOML could not be opened"}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return zero, &exitError{2, "external database TOML changed while opening"}
	}
	data, err := io.ReadAll(io.LimitReader(file, (16<<10)+1))
	if err != nil {
		return zero, &exitError{2, "external database TOML could not be read"}
	}
	spec, err := externaldatabase.Parse(data)
	if err != nil {
		return zero, &exitError{2, err.Error()}
	}
	return spec, nil
}

// Mutation output is metadata only, so a server response cannot echo a submitted
// credential object. The current connection remains available through show.
type externalDatabaseAccepted struct {
	ID            string     `json:"id"`
	DatabaseID    string     `json:"database_id,omitempty"`
	ApplicationID string     `json:"application_id,omitempty"`
	Revision      int64      `json:"revision"`
	Kind          string     `json:"kind,omitempty"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
}

func externalDatabaseRequestError(err error, credentials *externaldatabase.Credentials) error {
	if err == nil || credentials == nil {
		return err
	}
	message := err.Error()
	for _, value := range []string{credentials.Username, credentials.Password} {
		if value != "" {
			message = strings.ReplaceAll(message, value, "[redacted]")
		}
	}
	var exit *exitError
	if errors.As(err, &exit) {
		return &exitError{exit.code, message}
	}
	return errors.New(message)
}

func externalDatabaseCommand(ctx context.Context, c *client, project, environment string, args []string, flags externalDatabaseFlags) error {
	if len(args) < 1 || len(args) > 2 {
		return &exitError{2, "external-database requires list, show, rotate, delete, operation, trust, connections, refresh-review, refresh, disconnect-review or disconnect"}
	}
	action, id := args[0], ""
	if len(args) == 2 {
		id = args[1]
	}
	if action == "list" {
		if id != "" {
			return &exitError{2, "list does not take a connection ID"}
		}
	} else if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(id) {
		return &exitError{2, "provide the 32-character connection or operation ID"}
	}
	if flags.Disconnect || flags.File != "" {
		return &exitError{2, "creating and changing external database endpoint configuration is unavailable"}
	}
	if (flags.CredentialsFile != "" || flags.CredentialsStdin) && action != "rotate" {
		return &exitError{2, "credential input applies only to rotate"}
	}
	if flags.IdempotencyKey != "" && (len(flags.IdempotencyKey) < 8 || len(flags.IdempotencyKey) > 128) {
		return &exitError{2, "--idempotency-key must contain 8–128 characters"}
	}
	method, path := "GET", "/external-databases"
	var body any
	var credentials *externaldatabase.Credentials
	var out any
	switch action {
	case "list":
		if project == "" || environment == "" {
			return &exitError{2, "external-database list requires project and environment"}
		}
		path += "?" + url.Values{"project": {project}, "environment": {environment}}.Encode()
		out = &struct {
			Items []externaldatabase.Resource `json:"items"`
		}{}
	case "show":
		path += "/" + id
		out = &externaldatabase.Resource{}
	case "operation":
		path = "/external-database-operations/" + id
		out = &externaldatabase.Operation{}
	case "trust":
		path += "/" + id + "/trust"
		out = &struct {
			Mode            string    `json:"mode"`
			Hostname        string    `json:"hostname"`
			MinimumProtocol string    `json:"minimum_protocol"`
			TrustSource     string    `json:"trust_source"`
			Verified        bool      `json:"verified"`
			ObservedAt      time.Time `json:"observed_at"`
			Message         string    `json:"message"`
		}{}
	case "connections":
		path += "/" + id + "/connections"
		out = &store.DatabaseConnections{}
	case "rotate":
		if flags.Revision < 1 || flags.Name == "" {
			return &exitError{2, "rotate requires --revision and --name matching the connection"}
		}
		var current externaldatabase.Resource
		if err := c.request(ctx, "GET", path+"/"+id, nil, "", &current); err != nil {
			return err
		}
		var err error
		credentials, err = readExternalDatabaseCredentials(flags.CredentialsFile, flags.CredentialsStdin, os.Stdin)
		if err != nil {
			return err
		}
		if credentials == nil {
			return &exitError{2, "rotate requires --credentials-file or --credentials-stdin"}
		}
		method, path = "PUT", path+"/"+id
		body = map[string]any{"spec": current.Spec, "credentials": credentials, "expected_revision": flags.Revision, "confirm_name": flags.Name}
		out = &externalDatabaseAccepted{}
	case "delete":
		if flags.Revision < 1 || flags.Name == "" {
			return &exitError{2, "delete requires --revision and --name matching the connection"}
		}
		method, path, body = "DELETE", path+"/"+id, map[string]any{"expected_revision": flags.Revision, "confirm_name": flags.Name}
		out = &externalDatabaseAccepted{}
	case "refresh-review", "disconnect-review":
		if flags.ApplicationID == "" || flags.Service == "" || flags.Variable == "" {
			return &exitError{2, action + " requires --application-id, --service and --variable"}
		}
		method, path, body = "POST", path+"/"+id+"/connection-plan", map[string]any{"application_id": flags.ApplicationID, "service": flags.Service, "variable": flags.Variable, "disconnect": action == "disconnect-review"}
		out = &store.ExternalDatabaseConnectionPlan{}
	case "refresh", "disconnect":
		if flags.ReviewID == "" || flags.Name == "" {
			return &exitError{2, action + " requires --review-id and --name matching the reviewed application"}
		}
		method, path, body = "POST", path+"/"+id+"/connect", map[string]string{"review_id": flags.ReviewID, "confirm_application": flags.Name}
		out = &externalDatabaseAccepted{}
	default:
		return &exitError{2, "unknown external-database action"}
	}
	idem := flags.IdempotencyKey
	if method != "GET" && action != "disconnect-review" && action != "refresh-review" && idem == "" {
		idem = store.NewID()
	}
	if err := c.request(ctx, method, path, body, idem, out); err != nil {
		return externalDatabaseRequestError(err, credentials)
	}
	return printJSON(out)
}

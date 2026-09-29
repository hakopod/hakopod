package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/hakopod/hakopod/internal/spec"
	"golang.org/x/term"
)

func setupDeploymentSecrets(ctx context.Context, c *client, project, environment string, app spec.Application, missing []string, noninteractive bool) error {
	if len(missing) == 0 {
		return nil
	}
	if noninteractive || !term.IsTerminal(int(os.Stdin.Fd())) {
		return &exitError{2, "Missing application secrets: " + strings.Join(missing, ", ") + ". Save them through the dashboard or POST /api/v1/secrets/{name}, then retry. No deployment was submitted."}
	}
	reader := bufio.NewReader(os.Stdin)
	for _, name := range missing {
		body, err := readDeploymentSecret(reader, os.Stderr, func() ([]byte, error) {
			return term.ReadPassword(int(os.Stdin.Fd()))
		}, app, name)
		if err != nil {
			return err
		}
		query := url.Values{"project": {project}, "environment": {environment}, "application": {app.Name}}
		err = c.request(ctx, "POST", "/secrets/"+url.PathEscape(name)+"?"+query.Encode(), body, "", nil)
		delete(body, "value")
		if err != nil {
			return err
		}
	}
	return nil
}

// Separate terminal input from transport so credential handling is exercised
// without a real terminal or a provider account.
func readDeploymentSecret(reader *bufio.Reader, output io.Writer, readPassword func() ([]byte, error), app spec.Application, name string) (map[string]any, error) {
	providerCredential := false
	cacheCredential := false
	for _, service := range app.Services {
		if service.Actions != nil && service.Actions.Cache != nil && service.Actions.Cache.Credential == name && name != "" {
			cacheCredential = true
		}
		if service.Actions != nil && name != "" && (service.Actions.Credential == name || service.Actions.EffectiveJobsCredential() == name) {
			providerCredential = true
		}
	}
	if cacheCredential {
		fmt.Fprintf(output, "Secret %s: enter S3 access_key, secret_key and optional session_token JSON [v], or cancel [q]: ", name)
	} else if providerCredential {
		fmt.Fprintf(output, "Secret %s: enter a provider-issued token [v], or cancel [q]: ", name)
	} else {
		fmt.Fprintf(output, "Secret %s: enter a value [v], generate a new random password [g], or cancel [q]: ", name)
	}
	choice, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	switch strings.TrimSpace(strings.ToLower(choice)) {
	case "g":
		if cacheCredential {
			return nil, &exitError{2, "Cache credentials require S3-issued credentials. Random generation is not available; this secret was not saved."}
		}
		if providerCredential {
			return nil, &exitError{2, "Runner credentials require a provider-issued token. Random generation is not available; this secret was not saved."}
		}
		fmt.Fprintln(output, "Generating 32 random bytes as URL-safe Base64. This cannot create provider API tokens or certificates.")
		return map[string]any{"generate": true}, nil
	case "v", "":
		fmt.Fprint(output, "Value (hidden): ")
		value, err := readPassword()
		fmt.Fprintln(output)
		if err != nil {
			return nil, fmt.Errorf("secret input cancelled")
		}
		body := map[string]any{"value": string(value)}
		for i := range value {
			value[i] = 0
		}
		return body, nil
	default:
		return nil, &exitError{2, "Secret setup cancelled. No deployment was submitted."}
	}
}

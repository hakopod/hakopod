package main

import (
	"bufio"
	"context"
	"fmt"
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
		fmt.Fprintf(os.Stderr, "Secret %s: enter a value [v], generate a new random password [g], or cancel [q]: ", name)
		choice, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		body := map[string]any{}
		switch strings.TrimSpace(strings.ToLower(choice)) {
		case "g":
			fmt.Fprintln(os.Stderr, "Generating 32 random bytes as URL-safe Base64. This cannot create provider API tokens or certificates.")
			body["generate"] = true
		case "v", "":
			fmt.Fprint(os.Stderr, "Value (hidden): ")
			value, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return fmt.Errorf("secret input cancelled")
			}
			body["value"] = string(value)
			for i := range value {
				value[i] = 0
			}
		default:
			return &exitError{2, "Secret setup cancelled. No deployment was submitted."}
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

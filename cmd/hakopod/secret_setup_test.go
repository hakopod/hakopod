package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
)

func credentialApp(jobs string) spec.Application {
	return spec.Application{Name: "app", Services: map[string]spec.Service{
		"runner":   {Actions: &spec.Actions{Credential: "runner-management", JobsCredential: jobs}},
		"ordinary": {},
	}}
}

func TestSecretSetupNoninteractiveDoesNotSubmit(t *testing.T) {
	// A nil client proves no credential write or deploy call occurs in this branch.
	err := setupDeploymentSecrets(context.Background(), nil, "project", "main", spec.Application{Name: "app"}, []string{"database-password"}, true)
	if err == nil || !strings.Contains(err.Error(), "database-password") || !strings.Contains(err.Error(), "No deployment was submitted") {
		t.Fatal(err)
	}
	if err = setupDeploymentSecrets(context.Background(), nil, "project", "main", spec.Application{Name: "app"}, nil, true); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerCredentialsNeverOfferOrAcceptGeneration(t *testing.T) {
	for _, tc := range []struct {
		name string
		jobs string
	}{
		{name: "runner-management"},
		{name: "runner-management", jobs: "job-observation"},
		{name: "job-observation", jobs: "job-observation"},
		{name: "runner-management", jobs: "runner-management"},
	} {
		t.Run(tc.name+"/"+tc.jobs, func(t *testing.T) {
			var output bytes.Buffer
			read := false
			body, err := readDeploymentSecret(bufio.NewReader(strings.NewReader(" G \n")), &output, func() ([]byte, error) {
				read = true
				return nil, nil
			}, credentialApp(tc.jobs), tc.name)
			var failure *exitError
			if !errors.As(err, &failure) || failure.code != 2 || !strings.Contains(failure.message, "provider-issued token") {
				t.Fatal("provider credential generation must fail with a clear input error")
			}
			if body != nil || read {
				t.Fatal("rejected generation must not produce a request body or read a password")
			}
			if strings.Contains(output.String(), "[g]") || strings.Contains(output.String(), "Generating") {
				t.Fatal("provider credential prompt offered generation")
			}
			if !strings.Contains(output.String(), "provider-issued token [v]") {
				t.Fatal("provider credential prompt must explain the required input")
			}
		})
	}
}

func TestCacheCredentialsNeverOfferOrAcceptGeneration(t *testing.T) {
	app := credentialApp("")
	app.Services["cache-runner"] = spec.Service{Actions: &spec.Actions{Credential: "management", Cache: &spec.ActionsCache{Credential: "cache"}}}
	var output bytes.Buffer
	body, err := readDeploymentSecret(bufio.NewReader(strings.NewReader("g\n")), &output, func() ([]byte, error) { t.Fatal("generation must not read a credential"); return nil, nil }, app, "cache")
	if err == nil || body != nil || strings.Contains(output.String(), "[g]") || !strings.Contains(output.String(), "access_key, secret_key") {
		t.Fatal("cache prompt did not require real storage credentials")
	}
}

func TestCredentialGenerationChecksEveryPoolAndKeepsOrdinarySecretsAvailable(t *testing.T) {
	app := credentialApp("job-observation")
	app.Services["second"] = spec.Service{Actions: &spec.Actions{Credential: "shared-token", JobsCredential: "second-jobs"}}
	for _, name := range []string{"runner-management", "job-observation", "shared-token", "second-jobs", "ordinary-password"} {
		var output bytes.Buffer
		body, err := readDeploymentSecret(bufio.NewReader(strings.NewReader("g\n")), &output, func() ([]byte, error) {
			t.Fatal("generation must never read a password")
			return nil, nil
		}, app, name)
		if name == "ordinary-password" {
			if err != nil || body["generate"] != true || body["value"] != nil || !strings.Contains(output.String(), "[g]") {
				t.Fatal("ordinary passwords must retain explicit random generation")
			}
		} else if err == nil || body != nil || strings.Contains(output.String(), "[g]") {
			t.Fatal("a credential reference from any pool must prohibit generation")
		}
	}
}

func TestRunnerCredentialsAcceptHiddenValuesWithoutPrintingThem(t *testing.T) {
	for _, name := range []string{"runner-management", "job-observation"} {
		for _, choice := range []string{"v\n", "\n"} {
			var output bytes.Buffer
			value := []byte("synthetic-provider-token-for-test")
			body, err := readDeploymentSecret(bufio.NewReader(strings.NewReader(choice)), &output, func() ([]byte, error) {
				return value, nil
			}, credentialApp("job-observation"), name)
			if err != nil || body["value"] != "synthetic-provider-token-for-test" || body["generate"] != nil {
				t.Fatal("a supplied provider token must produce only the value request")
			}
			if strings.Contains(output.String(), "synthetic-provider-token-for-test") || !strings.Contains(output.String(), "Value (hidden)") {
				t.Fatal("provider token must remain hidden from terminal output")
			}
			if !bytes.Equal(value, make([]byte, len(value))) {
				t.Fatal("password input bytes were not cleared")
			}
			delete(body, "value")
		}
	}
}

func TestRunnerCredentialCancellationAndInputErrorsProduceNoRequest(t *testing.T) {
	for _, choice := range []string{"q\n", "unexpected\n", ""} {
		var output bytes.Buffer
		body, err := readDeploymentSecret(bufio.NewReader(strings.NewReader(choice)), &output, func() ([]byte, error) {
			t.Fatal("cancelled input must not ask for a password")
			return nil, nil
		}, credentialApp("job-observation"), "job-observation")
		if err == nil || body != nil {
			t.Fatal("cancellation or missing input must not create a request body")
		}
		if choice == "" && !errors.Is(err, io.EOF) {
			t.Fatal("missing terminal input must retain the input error")
		}
	}
	var output bytes.Buffer
	body, err := readDeploymentSecret(bufio.NewReader(strings.NewReader("v\n")), &output, func() ([]byte, error) {
		return nil, errors.New("synthetic-private-input-detail")
	}, credentialApp("job-observation"), "job-observation")
	if err == nil || body != nil || strings.Contains(err.Error()+output.String(), "synthetic-private-input-detail") {
		t.Fatal("failed hidden input must not expose its private error details or create a request")
	}
}

func TestSeparateRunnerCredentialsRemainRequiredInNoninteractiveMode(t *testing.T) {
	err := setupDeploymentSecrets(context.Background(), nil, "project", "main", credentialApp("job-observation"), []string{"runner-management", "job-observation"}, true)
	if err == nil || !strings.Contains(err.Error(), "runner-management, job-observation") || !strings.Contains(err.Error(), "No deployment was submitted") {
		t.Fatal("noninteractive runner setup must stop with missing reference names before any write")
	}
}

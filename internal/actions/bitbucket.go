package actions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const bitbucketOrigin = "https://api.bitbucket.org"
const bitbucketMaxRunnerPages = 5

var bitbucketOwnedName = regexp.MustCompile(`^hakopod-[a-f0-9]{32}$`)
var bitbucketLabel = regexp.MustCompile(`^[a-z0-9.]{1,64}$`)

// BitbucketCredential distinguishes an OAuth access token from an Atlassian API
// token. API tokens require the account email as HTTP Basic authentication.
type BitbucketCredential struct {
	AccessToken string `json:"-"`
	Email       string `json:"-"`
	APIToken    string `json:"-"`
}

type BitbucketClientOptions struct {
	Budget *RequestBudget
}

type BitbucketClient struct {
	target        ProviderTarget
	authorization string
	http          *http.Client
	budget        *RequestBudget
	budgetKey     [32]byte
}

var _ ProviderClient = (*BitbucketClient)(nil)

func NewBitbucketClient(target ProviderTarget, credential BitbucketCredential, options BitbucketClientOptions) (*BitbucketClient, error) {
	target, err := target.Canonical()
	if err != nil || target.Provider != ProviderBitbucket {
		return nil, errors.New("select a Bitbucket workspace UUID and optional repository UUID")
	}
	authorization, err := bitbucketAuthorization(credential)
	if err != nil {
		return nil, err
	}
	if options.Budget == nil {
		options.Budget = &RequestBudget{}
	}
	return &BitbucketClient{target: target, authorization: authorization, http: newBitbucketHTTP(), budget: options.Budget, budgetKey: sha256.Sum256([]byte("bitbucket\x00" + bitbucketOrigin + "\x00" + authorization))}, nil
}

func (c *BitbucketClient) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Provider: ProviderBitbucket, Available: false,
		Reason:            "Bitbucket runner execution, safe drain and registration revocation are not yet qualified",
		Cache:             ProviderCacheCapabilities{Reason: "Native cache and artifact isolation are not yet qualified"},
		Build:             ProviderBuildCapabilities{NativeArchitectures: []string{}, Reason: "Native and cross-architecture builds are not yet qualified"},
		Isolation:         ProviderIsolationCapabilities{Reason: "The public runner API does not establish a safe single-job drain boundary"},
		CancellationScope: CancellationNone,
	}
}

func (c *BitbucketClient) requireTarget(target ProviderTarget) error {
	canonical, err := target.Canonical()
	if err != nil || canonical.Provider != ProviderBitbucket || *canonical.Bitbucket != *c.target.Bitbucket {
		return &BitbucketError{Kind: "scope"}
	}
	return nil
}

func (c *BitbucketClient) runnerScope() string {
	target := c.target.Bitbucket
	if target.Repository != "" {
		return "/2.0/repositories/" + target.Workspace + "/" + target.Repository + "/pipelines-config/runners"
	}
	return "/2.0/workspaces/" + target.Workspace + "/pipelines-config/runners"
}

func (c *BitbucketClient) ownership(name string) string {
	digest := sha256.Sum256([]byte(c.runnerScope() + "\x00" + name))
	return "hakopod.owner." + hex.EncodeToString(digest[:20])
}

func (c *BitbucketClient) registrationLabels(name string, labels []string) ([]string, error) {
	// The public setup contract permits ten custom labels. Reserve one for
	// ownership, in addition to the two required platform labels.
	if !bitbucketOwnedName.MatchString(name) || len(labels) < 2 || len(labels) > 11 {
		return nil, errors.New("supply a unique managed Bitbucket name and at most nine custom labels")
	}
	seen := map[string]bool{}
	platforms := 0
	for _, label := range labels {
		if !bitbucketLabel.MatchString(label) || seen[label] || strings.HasPrefix(label, "hakopod.owner.") || label == "windows" || label == "macos" || label == "linux.shell" {
			return nil, errors.New("Bitbucket Docker runner labels must be unique lowercase letters, numbers and dots")
		}
		seen[label] = true
		if label == "linux" || label == "linux.arm64" {
			platforms++
		}
	}
	if !seen["self.hosted"] || platforms != 1 {
		return nil, errors.New("Bitbucket Docker runners require self.hosted and exactly one of linux or linux.arm64")
	}
	return append(append([]string{}, labels...), c.ownership(name)), nil
}

// BitbucketManagerConfig is private registration material, never an API response.
// The OAuth endpoint and audience are retained as data; this adapter never sends
// credentials to a provider-supplied URL. Native bootstrap is not yet qualified.
type BitbucketManagerConfig struct {
	SchemaVersion int             `json:"schema_version"`
	Target        BitbucketTarget `json:"target"`
	RunnerID      string          `json:"runner_id"`
	Name          string          `json:"name"`
	OAuthClientID string          `json:"oauth_client_id"`
	OAuthSecret   string          `json:"oauth_client_secret"`
	TokenEndpoint string          `json:"token_endpoint,omitempty"`
	Audience      string          `json:"audience,omitempty"`
}

type bitbucketRunner struct {
	UUID   string   `json:"uuid"`
	Name   string   `json:"name"`
	Labels []string `json:"labels"`
	State  *struct {
		Status   string `json:"status"`
		Cordoned *bool  `json:"cordoned"`
	} `json:"state"`
	OAuth *struct {
		ID            string `json:"id"`
		Secret        string `json:"secret"`
		TokenEndpoint string `json:"token_endpoint"`
		Audience      string `json:"audience"`
	} `json:"oauth_client"`
}

func (c *BitbucketClient) Register(ctx context.Context, target ProviderTarget, name string, labels []string) (ProviderRegistration, error) {
	if err := c.requireTarget(target); err != nil {
		return ProviderRegistration{}, err
	}
	labels, err := c.registrationLabels(name, labels)
	if err != nil {
		return ProviderRegistration{}, err
	}
	ctx, release, err := c.reserve(ctx, 1)
	if err != nil {
		return ProviderRegistration{}, err
	}
	defer release()
	var runner bitbucketRunner
	// Public runner CRUD uses the pipeline_runner representation. Do not send a
	// caller-selected UUID: its idempotency semantics have not been established.
	if err = c.json(ctx, http.MethodPost, c.runnerScope(), nil, map[string]any{"name": name, "labels": labels}, http.StatusOK, &runner); err != nil {
		return ProviderRegistration{}, err
	}
	if err = c.verifyRunner(&runner, "", name); err != nil || runner.OAuth == nil || !bitbucketPrivateString(runner.OAuth.ID, 1, 256) || !bitbucketPrivateString(runner.OAuth.Secret, 16, 4096) || (runner.OAuth.TokenEndpoint != "" && runner.OAuth.TokenEndpoint != "https://bitbucket.org/site/oauth2/access_token") || !bitbucketOptionalPrivateString(runner.OAuth.Audience, 2048) {
		return ProviderRegistration{}, &BitbucketError{Kind: "response", Ambiguous: true}
	}
	if len(runner.Labels) != len(labels) {
		return ProviderRegistration{}, &BitbucketError{Kind: "identity", Ambiguous: true}
	}
	for _, expected := range labels {
		found := false
		for _, actual := range runner.Labels {
			found = found || expected == actual
		}
		if !found {
			return ProviderRegistration{}, &BitbucketError{Kind: "identity", Ambiguous: true}
		}
	}
	config, err := json.Marshal(BitbucketManagerConfig{SchemaVersion: 1, Target: *c.target.Bitbucket, RunnerID: runner.UUID, Name: name, OAuthClientID: runner.OAuth.ID, OAuthSecret: runner.OAuth.Secret, TokenEndpoint: runner.OAuth.TokenEndpoint, Audience: runner.OAuth.Audience})
	if err != nil {
		return ProviderRegistration{}, &BitbucketError{Kind: "response", Ambiguous: true}
	}
	// Runner OAuth credentials have no documented independent delete authority.
	// Cleanup must retain the original scoped management credential reference.
	result := ProviderRegistration{Runner: bitbucketObservedRunner(runner), ManagerConfig: config}
	if result.Validate(name) != nil {
		return ProviderRegistration{}, &BitbucketError{Kind: "response", Ambiguous: true}
	}
	return result, nil
}

func (c *BitbucketClient) verifyRunner(runner *bitbucketRunner, id, name string) error {
	canonical, ok := canonicalProviderUUID(runner.UUID)
	if !ok || (id != "" && canonical != id) || !bitbucketOwnedName.MatchString(runner.Name) || (name != "" && runner.Name != name) || len(runner.Labels) > 12 {
		return &BitbucketError{Kind: "identity"}
	}
	runner.UUID = canonical
	seen := map[string]bool{}
	for _, label := range runner.Labels {
		if !bitbucketLabel.MatchString(label) || seen[label] {
			return &BitbucketError{Kind: "response"}
		}
		seen[label] = true
	}
	if !seen[c.ownership(runner.Name)] {
		return &BitbucketError{Kind: "identity"}
	}
	if runner.State == nil {
		return &BitbucketError{Kind: "response"}
	}
	switch runner.State.Status {
	case "UNREGISTERED", "ONLINE", "OFFLINE", "DISABLED", "ENABLED", "UNHEALTHY":
		return nil
	default:
		return &BitbucketError{Kind: "response"}
	}
}

func bitbucketObservedRunner(runner bitbucketRunner) ProviderRunner {
	// The documented API has no busy/idle field. Conservative busy prevents
	// callers from interpreting an offline or cordoned runner as safe to recycle.
	return ProviderRunner{ID: runner.UUID, Name: runner.Name, Status: strings.ToLower(runner.State.Status), Busy: true, ObservedAt: time.Now()}
}

type bitbucketRunnerPage struct {
	Values  []bitbucketRunner `json:"values"`
	Page    *int              `json:"page"`
	PageLen *int              `json:"pagelen"`
	Size    *int              `json:"size"`
	Next    string            `json:"next"`
}

func (c *BitbucketClient) inventory(ctx context.Context, id, name string) ([]bitbucketRunner, error) {
	query := url.Values{"pagelen": {"100"}}
	seenIDs, seenPages := map[string]bool{}, map[string]bool{}
	matches := []bitbucketRunner{}
	total := 0
	for page := 1; page <= bitbucketMaxRunnerPages; page++ {
		encoded := query.Encode()
		if seenPages[encoded] {
			return nil, &BitbucketError{Kind: "response"}
		}
		seenPages[encoded] = true
		var result bitbucketRunnerPage
		if err := c.json(ctx, http.MethodGet, c.runnerScope(), query, nil, http.StatusOK, &result); err != nil {
			return nil, err
		}
		total += len(result.Values)
		if result.Values == nil || len(result.Values) > 100 || (result.Page != nil && *result.Page != page) || (result.PageLen != nil && (*result.PageLen < 1 || *result.PageLen > 100 || len(result.Values) > *result.PageLen)) || (result.Size != nil && (*result.Size < total || *result.Size > 100*bitbucketMaxRunnerPages)) {
			return nil, &BitbucketError{Kind: "bounds"}
		}
		for _, runner := range result.Values {
			canonical, ok := canonicalProviderUUID(runner.UUID)
			if !ok || seenIDs[canonical] || len(runner.Name) > 1024 {
				return nil, &BitbucketError{Kind: "response"}
			}
			seenIDs[canonical] = true
			if (id != "" && canonical == id) || (name != "" && runner.Name == name) {
				if err := c.verifyRunner(&runner, id, name); err != nil {
					return nil, err
				}
				matches = append(matches, runner)
			}
		}
		if result.Next == "" {
			if result.Size != nil && *result.Size != total {
				return nil, &BitbucketError{Kind: "response"}
			}
			return matches, nil
		}
		if len(result.Values) == 0 || page == bitbucketMaxRunnerPages {
			return nil, &BitbucketError{Kind: "bounds"}
		}
		var err error
		query, err = c.nextPage(result.Next)
		if err != nil {
			return nil, err
		}
	}
	return nil, &BitbucketError{Kind: "bounds"}
}

func (c *BitbucketClient) nextPage(raw string) (url.Values, error) {
	if len(raw) > 4096 || strings.ContainsAny(raw, "\\\r\n") {
		return nil, &BitbucketError{Kind: "scope"}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "api.bitbucket.org" || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.Path != c.runnerScope() {
		return nil, &BitbucketError{Kind: "scope"}
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query) < 1 || len(query) > 2 {
		return nil, &BitbucketError{Kind: "scope"}
	}
	for key, values := range query {
		if (key != "page" && key != "pagelen") || len(values) != 1 || len(values[0]) == 0 || len(values[0]) > 1024 || strings.IndexFunc(values[0], func(r rune) bool { return r <= ' ' || r >= 127 }) >= 0 {
			return nil, &BitbucketError{Kind: "scope"}
		}
	}
	if query.Get("page") == "" {
		return nil, &BitbucketError{Kind: "scope"}
	}
	if value := query.Get("pagelen"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 100 || strconv.Itoa(n) != value {
			return nil, &BitbucketError{Kind: "scope"}
		}
	}
	return query, nil
}

func (c *BitbucketClient) FindOwned(ctx context.Context, target ProviderTarget, name string) ([]ProviderRunner, error) {
	if err := c.requireTarget(target); err != nil {
		return nil, err
	}
	if !bitbucketOwnedName.MatchString(name) {
		return nil, &BitbucketError{Kind: "identity"}
	}
	ctx, release, err := c.reserve(ctx, bitbucketMaxRunnerPages)
	if err != nil {
		return nil, err
	}
	defer release()
	runners, err := c.inventory(ctx, "", name)
	if err != nil {
		return nil, err
	}
	result := make([]ProviderRunner, 0, len(runners))
	for _, runner := range runners {
		result = append(result, bitbucketObservedRunner(runner))
	}
	return result, nil
}

func (c *BitbucketClient) getOwned(ctx context.Context, id, name string) (bitbucketRunner, error) {
	runners, err := c.inventory(ctx, id, name)
	if err != nil {
		return bitbucketRunner{}, err
	}
	if len(runners) != 1 || runners[0].UUID != id {
		return bitbucketRunner{}, &BitbucketError{Kind: "identity"}
	}
	var runner bitbucketRunner
	if err := c.json(ctx, http.MethodGet, c.runnerScope()+"/"+id, nil, nil, http.StatusOK, &runner); err != nil {
		return bitbucketRunner{}, err
	}
	if err := c.verifyRunner(&runner, id, runners[0].Name); err != nil {
		return bitbucketRunner{}, err
	}
	return runner, nil
}

func (c *BitbucketClient) Get(ctx context.Context, target ProviderTarget, id string) (ProviderRunner, error) {
	return c.GetOwned(ctx, target, id, "")
}

func (c *BitbucketClient) GetOwned(ctx context.Context, target ProviderTarget, id, name string) (ProviderRunner, error) {
	if err := c.requireTarget(target); err != nil {
		return ProviderRunner{}, err
	}
	id, ok := canonicalProviderUUID(id)
	if !ok || (name != "" && !bitbucketOwnedName.MatchString(name)) {
		return ProviderRunner{}, &BitbucketError{Kind: "identity"}
	}
	ctx, release, err := c.reserve(ctx, bitbucketMaxRunnerPages+1)
	if err != nil {
		return ProviderRunner{}, err
	}
	defer release()
	runner, err := c.getOwned(ctx, id, name)
	if err != nil {
		return ProviderRunner{}, err
	}
	return bitbucketObservedRunner(runner), nil
}

func (c *BitbucketClient) Delete(ctx context.Context, target ProviderTarget, id string) error {
	return c.DeleteOwned(ctx, target, id, "")
}

// DeleteOwned can remove only an unused registration. Its caller must first
// fence the durable slot and ensure no manager can start with this credential.
// No public API contract proves safe cleanup of a previously started runner.
func (c *BitbucketClient) DeleteOwned(ctx context.Context, target ProviderTarget, id, name string) error {
	if err := c.requireTarget(target); err != nil {
		return err
	}
	id, ok := canonicalProviderUUID(id)
	if !ok || (name != "" && !bitbucketOwnedName.MatchString(name)) {
		return &BitbucketError{Kind: "identity"}
	}
	ctx, release, err := c.reserve(ctx, bitbucketMaxRunnerPages+2)
	if err != nil {
		return err
	}
	defer release()
	runner, err := c.getOwned(ctx, id, name)
	if err != nil {
		return err
	}
	if runner.State.Status != "UNREGISTERED" {
		return &BitbucketError{Kind: "drain"}
	}
	return c.json(ctx, http.MethodDelete, c.runnerScope()+"/"+id, nil, nil, http.StatusNoContent, nil)
}

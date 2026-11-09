// Package awsquery calls AWS Query APIs (EC2, STS, RDS) over net/http with
// SigV4 from the core SDK module, so callers add no service SDK modules to
// go.mod. Responses are decoded with encoding/xml into the fields the caller
// names.
package awsquery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

const maxResponse = 16 << 20

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

var defaultHTTP = &http.Client{Timeout: 30 * time.Second, CheckRedirect: noRedirect}

// Instance metadata is link-local: never through a proxy, never slow.
var metadataHTTP = &http.Client{Transport: &http.Transport{}, Timeout: 2 * time.Second, CheckRedirect: noRedirect}

// Client signs calls with Credentials for one region.
type Client struct {
	HTTP        *http.Client // a 30-second client without redirects if nil
	Credentials aws.CredentialsProvider
	Region      string
	// Endpoint overrides https://<service>.<region>.amazonaws.com/ in tests.
	Endpoint func(service string) string
}

// Error is an AWS error response. Message can echo request details, so
// Error() returns only the code.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("status %d", e.Status)
	}
	return e.Code
}

func (c Client) url(service string) string {
	if c.Endpoint != nil {
		return c.Endpoint(service)
	}
	return "https://" + service + "." + c.Region + ".amazonaws.com/"
}

// Call posts one Query action and decodes the XML response into out.
func (c Client) Call(ctx context.Context, service, action, version string, params url.Values, out any) error {
	params.Set("Action", action)
	params.Set("Version", version)
	body := params.Encode()
	sum := sha256.Sum256([]byte(body))
	return do(ctx, c.HTTP, func() (*http.Request, error) {
		creds, err := c.Credentials.Retrieve(ctx)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(service), strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
		return req, v4.NewSigner().SignHTTP(ctx, creds, req, hex.EncodeToString(sum[:]), service, c.Region, time.Now())
	}, out)
}

// Only throttling and 503s are retried: AWS refused the request, so even a
// call that is not idempotent, such as CreateFleet, did not happen.
var retryable = []string{"Throttling", "ThrottlingException", "RequestLimitExceeded", "RequestThrottled", "TooManyRequestsException", "Unavailable", "ServiceUnavailable"}

func do(ctx context.Context, client *http.Client, build func() (*http.Request, error), out any) error {
	if client == nil {
		client = defaultHTTP
	}
	for attempt := 0; ; attempt++ {
		req, err := build()
		if err != nil {
			return err
		}
		err = once(client, req, out)
		var e *Error
		if attempt == 2 || !errors.As(err, &e) || (e.Status != http.StatusServiceUnavailable && !slices.Contains(retryable, e.Code)) {
			return err
		}
		wait := time.Duration(200<<attempt)*time.Millisecond + rand.N(100*time.Millisecond)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

func once(client *http.Client, req *http.Request, out any) error {
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil {
		return err
	}
	if len(data) > maxResponse {
		return fmt.Errorf("AWS response larger than %d bytes", maxResponse)
	}
	if res.StatusCode != http.StatusOK {
		// EC2: <Response><Errors><Error>; STS and RDS: <ErrorResponse><Error>.
		var body struct {
			EC2   []errorXML `xml:"Errors>Error"`
			Query []errorXML `xml:"Error"`
		}
		_ = xml.Unmarshal(data, &body)
		e := &Error{Status: res.StatusCode}
		if all := append(body.EC2, body.Query...); len(all) > 0 {
			e.Code, e.Message = all[0].Code, all[0].Message
		}
		return e
	}
	return xml.Unmarshal(data, out)
}

type errorXML struct {
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

type stsCredentials struct {
	AccessKeyID     string    `xml:"AccessKeyId"`
	SecretAccessKey string    `xml:"SecretAccessKey"`
	SessionToken    string    `xml:"SessionToken"`
	Expiration      time.Time `xml:"Expiration"`
}

func (s stsCredentials) credentials(source string) (aws.Credentials, error) {
	if s.AccessKeyID == "" || s.SecretAccessKey == "" {
		return aws.Credentials{}, errors.New("STS returned no credentials")
	}
	return aws.Credentials{AccessKeyID: s.AccessKeyID, SecretAccessKey: s.SecretAccessKey, SessionToken: s.SessionToken,
		Source: source, CanExpire: !s.Expiration.IsZero(), Expires: s.Expiration}, nil
}

// AssumeRole returns an hour of role credentials from the regional STS
// endpoint, signed with c's own credentials and refreshed five minutes before
// they expire.
func AssumeRole(c Client, role, externalID, session string) *aws.CredentialsCache {
	return aws.NewCredentialsCache(aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
		params := url.Values{"RoleArn": {role}, "RoleSessionName": {session}, "DurationSeconds": {"3600"}}
		if externalID != "" {
			params.Set("ExternalId", externalID)
		}
		var out struct {
			Credentials stsCredentials `xml:"AssumeRoleResult>Credentials"`
		}
		if err := c.Call(ctx, "sts", "AssumeRole", "2011-06-15", params, &out); err != nil {
			return aws.Credentials{}, err
		}
		return out.Credentials.credentials("assume-role")
	}), func(o *aws.CredentialsCacheOptions) { o.ExpiryWindow = 5 * time.Minute })
}

// Identity resolves credentials and region the way the installation runs:
// static environment keys, a web identity token (EKS, the projected
// hakopod-aws-identity token, GitHub OIDC), then the EC2 instance role over
// IMDSv2. Shared config files and SSO are not read.
type Identity struct {
	HTTP        *http.Client        // for STS; a 30-second client if nil
	Getenv      func(string) string // os.Getenv if nil
	IMDS        string              // http://169.254.169.254 unless tested
	STS         string              // https://sts.<region>.amazonaws.com/ unless tested
	SessionName string              // for web identity; "hakopod" if empty
}

// Provider resolves the identity on every Retrieve. Use it to sign calls that
// are cached themselves, such as AssumeRole, or wrap it in a cache.
func (a Identity) Provider(region string) aws.CredentialsProvider {
	return aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
		creds, _, err := a.Resolve(ctx, region)
		return creds, err
	})
}

func (a Identity) Resolve(ctx context.Context, region string) (aws.Credentials, string, error) {
	getenv := a.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	imds := func() (string, error) { return "", errors.New("instance metadata disabled") }
	if !strings.EqualFold(getenv("AWS_EC2_METADATA_DISABLED"), "true") {
		token := ""
		imds = func() (string, error) {
			if token == "" {
				t, err := a.metadata(ctx, http.MethodPut, "/latest/api/token", "")
				if err != nil {
					return "", err
				}
				token = t
			}
			return token, nil
		}
	}
	for _, name := range []string{"AWS_REGION", "AWS_DEFAULT_REGION"} {
		if region == "" {
			region = getenv(name)
		}
	}
	if region == "" {
		if token, err := imds(); err == nil {
			region, _ = a.metadata(ctx, http.MethodGet, "/latest/meta-data/placement/region", token)
		}
	}
	if region == "" {
		return aws.Credentials{}, "", errors.New("no region")
	}
	if key, secret := getenv("AWS_ACCESS_KEY_ID"), getenv("AWS_SECRET_ACCESS_KEY"); key != "" && secret != "" {
		return aws.Credentials{AccessKeyID: key, SecretAccessKey: secret, SessionToken: getenv("AWS_SESSION_TOKEN"), Source: "environment"}, region, nil
	}
	if file, role := getenv("AWS_WEB_IDENTITY_TOKEN_FILE"), getenv("AWS_ROLE_ARN"); file != "" && role != "" {
		creds, err := a.webIdentity(ctx, region, file, role)
		return creds, region, err
	}
	token, err := imds()
	if err != nil {
		return aws.Credentials{}, "", err
	}
	name, err := a.metadata(ctx, http.MethodGet, "/latest/meta-data/iam/security-credentials/", token)
	if name = strings.TrimSpace(strings.SplitN(name, "\n", 2)[0]); err != nil || name == "" {
		return aws.Credentials{}, "", errors.New("no instance role")
	}
	raw, err := a.metadata(ctx, http.MethodGet, "/latest/meta-data/iam/security-credentials/"+url.PathEscape(name), token)
	if err != nil {
		return aws.Credentials{}, "", err
	}
	var role struct {
		AccessKeyId, SecretAccessKey, Token string
		Expiration                          time.Time
	}
	if err := json.Unmarshal([]byte(raw), &role); err != nil || role.AccessKeyId == "" {
		return aws.Credentials{}, "", errors.New("invalid instance role credentials")
	}
	return aws.Credentials{AccessKeyID: role.AccessKeyId, SecretAccessKey: role.SecretAccessKey, SessionToken: role.Token, Source: "imds",
		CanExpire: !role.Expiration.IsZero(), Expires: role.Expiration}, region, nil
}

func (a Identity) metadata(ctx context.Context, method, path, token string) (string, error) {
	base := a.IMDS
	if base == "" {
		base = "http://169.254.169.254"
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, nil)
	if err != nil {
		return "", err
	}
	if method == http.MethodPut {
		req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "300")
	} else {
		req.Header.Set("X-aws-ec2-metadata-token", token)
	}
	res, err := metadataHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err == nil && res.StatusCode != http.StatusOK {
		err = fmt.Errorf("instance metadata status %d", res.StatusCode)
	}
	return string(data), err
}

// AssumeRoleWithWebIdentity is unsigned: the token is the proof.
func (a Identity) webIdentity(ctx context.Context, region, file, role string) (aws.Credentials, error) {
	token, err := os.ReadFile(file)
	if err != nil {
		return aws.Credentials{}, err
	}
	endpoint := a.STS
	if endpoint == "" {
		endpoint = "https://sts." + region + ".amazonaws.com/"
	}
	session := a.SessionName
	if session == "" {
		session = "hakopod"
	}
	body := url.Values{"Action": {"AssumeRoleWithWebIdentity"}, "Version": {"2011-06-15"}, "RoleArn": {role},
		"RoleSessionName": {session}, "WebIdentityToken": {strings.TrimSpace(string(token))}}.Encode()
	var out struct {
		Credentials stsCredentials `xml:"AssumeRoleWithWebIdentityResult>Credentials"`
	}
	err = do(ctx, a.HTTP, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
		}
		return req, err
	}, &out)
	if err != nil {
		return aws.Credentials{}, fmt.Errorf("sts:AssumeRoleWithWebIdentity: %w", err)
	}
	return out.Credentials.credentials("web-identity")
}

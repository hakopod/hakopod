package awsquery

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func static(id string) aws.CredentialsProvider {
	return aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: id, SecretAccessKey: "secret", SessionToken: "session"}, nil
	})
}

func TestCallSignsAndDecodesErrors(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDTEST/") || !strings.Contains(auth, "/us-east-1/ec2/aws4_request") ||
			r.Header.Get("X-Amz-Security-Token") != "session" || r.Header.Get("X-Amz-Date") == "" {
			t.Errorf("signature %q", auth)
		}
		switch r.Form.Get("InstanceId.1") {
		case "i-throttled":
			if calls.Add(1) == 1 {
				w.WriteHeader(503)
				w.Write([]byte(`<Response><Errors><Error><Code>RequestLimitExceeded</Code><Message>Request limit exceeded.</Message></Error></Errors><RequestID>1</RequestID></Response>`))
				return
			}
			w.Write([]byte(`<DescribeInstancesResponse><requestId>1</requestId><nextToken>next</nextToken></DescribeInstancesResponse>`))
		case "i-denied":
			calls.Add(1)
			w.WriteHeader(403)
			w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<Response><Errors><Error><Code>UnauthorizedOperation</Code><Message>You are not authorized to perform this operation. Encoded authorization failure message: abc</Message></Error></Errors><RequestID>2</RequestID></Response>`))
		case "i-redirect":
			http.Redirect(w, r, "https://example.com/", http.StatusFound)
		case "i-huge":
			w.Write([]byte("<a>" + strings.Repeat("x", maxResponse) + "</a>"))
		}
	}))
	defer server.Close()
	c := Client{HTTP: &http.Client{CheckRedirect: noRedirect}, Credentials: static("AKIDTEST"), Region: "us-east-1", Endpoint: func(string) string { return server.URL + "/" }}
	call := func(id string, out any) error {
		return c.Call(context.Background(), "ec2", "DescribeInstances", "2016-11-15", map[string][]string{"InstanceId.1": {id}}, out)
	}

	var out struct {
		NextToken string `xml:"nextToken"`
	}
	if err := call("i-throttled", &out); err != nil || out.NextToken != "next" || calls.Load() != 2 {
		t.Fatal("throttled call was not retried", out, err, calls.Load())
	}
	calls.Store(0)
	var e *Error
	if err := call("i-denied", &out); !errors.As(err, &e) || e.Code != "UnauthorizedOperation" || e.Status != 403 || err.Error() != "UnauthorizedOperation" || calls.Load() != 1 {
		t.Fatal("access denied", err, calls.Load())
	}
	if err := call("i-redirect", &out); !errors.As(err, &e) || e.Status != http.StatusFound {
		t.Fatal("redirect followed", err)
	}
	if err := call("i-huge", &out); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatal("unbounded response", err)
	}
}

func TestAssumeRole(t *testing.T) {
	var calls atomic.Int32
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		calls.Add(1)
		if !strings.Contains(r.Header.Get("Authorization"), "Credential=AKIDBASE/") || !strings.Contains(r.Header.Get("Authorization"), "/eu-west-1/sts/aws4_request") ||
			r.Form.Get("Action") != "AssumeRole" || r.Form.Get("Version") != "2011-06-15" || r.Form.Get("RoleArn") != "arn:aws:iam::123456789012:role/hakopod-actions" ||
			r.Form.Get("ExternalId") != "hakopod-x" || r.Form.Get("RoleSessionName") != "hakopod-actions" || r.Form.Get("DurationSeconds") != "3600" {
			w.WriteHeader(403)
			w.Write([]byte(`<ErrorResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><Error><Type>Sender</Type><Code>AccessDenied</Code><Message>not authorized</Message></Error><RequestId>1</RequestId></ErrorResponse>`))
			return
		}
		// From the AssumeRole API reference.
		w.Write([]byte(`<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
<AssumeRoleResult>
  <SourceIdentity>DevUser123</SourceIdentity>
  <AssumedRoleUser>
    <Arn>arn:aws:sts::123456789012:assumed-role/hakopod-actions/hakopod-actions</Arn>
    <AssumedRoleId>ARO123EXAMPLE123:hakopod-actions</AssumedRoleId>
  </AssumedRoleUser>
  <Credentials>
    <AccessKeyId>ASIAIOSFODNN7EXAMPLE</AccessKeyId>
    <SecretAccessKey>wJalrXUtnFEMI/K7MDENG/bPxRfiCYzEXAMPLEKEY</SecretAccessKey>
    <SessionToken>AQoDYXdzEPT//////////wEXAMPLEtc764</SessionToken>
    <Expiration>` + expires.Format(time.RFC3339) + `</Expiration>
  </Credentials>
  <PackedPolicySize>8</PackedPolicySize>
</AssumeRoleResult>
<ResponseMetadata><RequestId>c6104cbe-af31-11e0-8154-cbc7ccf896c7</RequestId></ResponseMetadata>
</AssumeRoleResponse>`))
	}))
	defer sts.Close()
	base := Client{Credentials: static("AKIDBASE"), Region: "eu-west-1", Endpoint: func(service string) string { return sts.URL + "/" + service }}
	role := AssumeRole(base, "arn:aws:iam::123456789012:role/hakopod-actions", "hakopod-x", "hakopod-actions")
	for range 2 {
		creds, err := role.Retrieve(context.Background())
		if err != nil || creds.AccessKeyID != "ASIAIOSFODNN7EXAMPLE" || creds.SessionToken != "AQoDYXdzEPT//////////wEXAMPLEtc764" || !creds.CanExpire || !creds.Expires.Equal(expires.Add(-5*time.Minute)) {
			t.Fatal(creds, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("assumed credentials were not cached", calls.Load())
	}
	wrong := AssumeRole(base, "arn:aws:iam::123456789012:role/hakopod-actions", "", "hakopod-actions")
	var e *Error
	if _, err := wrong.Retrieve(context.Background()); !errors.As(err, &e) || e.Code != "AccessDenied" {
		t.Fatal("STS error", err)
	}
}

func TestIdentity(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	os.WriteFile(token, []byte("jwt\n"), 0o600)
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Header.Get("Authorization") != "" || r.Form.Get("Action") != "AssumeRoleWithWebIdentity" || r.Form.Get("WebIdentityToken") != "jwt" || r.Form.Get("RoleArn") != "arn:aws:iam::1:role/r" || r.Form.Get("RoleSessionName") != "hakopod" {
			http.Error(w, "bad", 400)
			return
		}
		w.Write([]byte(`<AssumeRoleWithWebIdentityResponse><AssumeRoleWithWebIdentityResult><Credentials>
<AccessKeyId>ASIAWEB</AccessKeyId><SecretAccessKey>s</SecretAccessKey><SessionToken>t</SessionToken><Expiration>2026-11-09T12:00:00Z</Expiration>
</Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`))
	}))
	defer sts.Close()
	imds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			if r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds") == "" {
				http.Error(w, "no ttl", 400)
				return
			}
			w.Write([]byte("tok"))
			return
		}
		if r.Header.Get("X-aws-ec2-metadata-token") != "tok" {
			http.Error(w, "no token", 401)
			return
		}
		switch r.URL.Path {
		case "/latest/meta-data/placement/region":
			w.Write([]byte("eu-west-1"))
		case "/latest/meta-data/iam/security-credentials/":
			w.Write([]byte("node-role"))
		case "/latest/meta-data/iam/security-credentials/node-role":
			w.Write([]byte(`{"Code":"Success","LastUpdated":"2026-11-09T10:00:00Z","Type":"AWS-HMAC","AccessKeyId":"ASIAIMDS","SecretAccessKey":"s","Token":"t","Expiration":"2026-11-09T16:00:00Z"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer imds.Close()
	env := func(values map[string]string) func(string) string { return func(k string) string { return values[k] } }

	static := Identity{IMDS: imds.URL, Getenv: env(map[string]string{"AWS_ACCESS_KEY_ID": "AKIDENV", "AWS_SECRET_ACCESS_KEY": "s", "AWS_SESSION_TOKEN": "t"})}
	creds, region, err := static.Resolve(context.Background(), "us-east-1")
	if err != nil || creds.AccessKeyID != "AKIDENV" || creds.SessionToken != "t" || region != "us-east-1" {
		t.Fatalf("environment: %+v %q %v", creds, region, err)
	}
	web := Identity{IMDS: imds.URL, STS: sts.URL,
		Getenv: env(map[string]string{"AWS_REGION": "us-east-2", "AWS_WEB_IDENTITY_TOKEN_FILE": token, "AWS_ROLE_ARN": "arn:aws:iam::1:role/r", "AWS_EC2_METADATA_DISABLED": "true"})}
	creds, region, err = web.Resolve(context.Background(), "")
	if err != nil || creds.AccessKeyID != "ASIAWEB" || creds.SessionToken != "t" || region != "us-east-2" || !creds.CanExpire {
		t.Fatalf("web identity: %+v %q %v", creds, region, err)
	}
	instance := Identity{IMDS: imds.URL, Getenv: env(nil)}
	creds, region, err = instance.Resolve(context.Background(), "")
	if err != nil || creds.AccessKeyID != "ASIAIMDS" || region != "eu-west-1" || !creds.Expires.Equal(time.Date(2026, 11, 9, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("imds: %+v %q %v", creds, region, err)
	}
	if creds, err := instance.Provider("eu-west-1").Retrieve(context.Background()); err != nil || creds.AccessKeyID != "ASIAIMDS" {
		t.Fatal("provider", creds, err)
	}
	disabled := Identity{IMDS: imds.URL, Getenv: env(map[string]string{"AWS_EC2_METADATA_DISABLED": "true"})}
	if _, _, err := disabled.Resolve(context.Background(), "us-east-1"); err == nil {
		t.Fatal("expected no identity")
	}
}

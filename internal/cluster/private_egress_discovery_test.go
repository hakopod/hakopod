package cluster

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func fakeAWS(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKIDTEST/") {
			t.Errorf("unsigned %s request", r.Form.Get("Action"))
		}
		instance := func(id string) string {
			return `<DBInstance><DBInstanceIdentifier>` + id + `</DBInstanceIdentifier><DBClusterIdentifier>orders</DBClusterIdentifier>
<Engine>aurora-postgresql</Engine><Endpoint><Port>5432</Port></Endpoint><DBSubnetGroup><Subnets>
<Subnet><SubnetIdentifier>a</SubnetIdentifier></Subnet><Subnet><SubnetIdentifier>b</SubnetIdentifier></Subnet>
<Subnet><SubnetIdentifier>c</SubnetIdentifier></Subnet></Subnets></DBSubnetGroup></DBInstance>`
		}
		switch r.Form.Get("Action") {
		case "DescribeDBInstances":
			if r.Form.Get("Marker") == "" {
				w.Write([]byte(`<DescribeDBInstancesResponse><DescribeDBInstancesResult><DBInstances>` + instance("orders-1") + `</DBInstances><Marker>next</Marker></DescribeDBInstancesResult></DescribeDBInstancesResponse>`))
			} else {
				w.Write([]byte(`<DescribeDBInstancesResponse><DescribeDBInstancesResult><DBInstances>` + instance("orders-2") + `</DBInstances></DescribeDBInstancesResult></DescribeDBInstancesResponse>`))
			}
		case "DescribeSubnets":
			cidrs := map[string]string{"a": "172.31.0.0/20", "b": "172.31.38.0/24", "c": "10.42.0.0/24"}
			body := `<DescribeSubnetsResponse><subnetSet>`
			for i := 1; r.Form.Get("SubnetId."+strconv.Itoa(i)) != ""; i++ {
				id := r.Form.Get("SubnetId." + strconv.Itoa(i))
				body += `<item><subnetId>` + id + `</subnetId><cidrBlock>` + cidrs[id] + `</cidrBlock></item>`
			}
			w.Write([]byte(body + `</subnetSet></DescribeSubnetsResponse>`))
		default:
			http.Error(w, "unknown action", 400)
		}
	}))
}

func TestDiscoverPrivateEgressExcludesNodeSubnets(t *testing.T) {
	server := fakeAWS(t)
	defer server.Close()
	q := awsQuery{client: server.Client(), creds: aws.Credentials{AccessKeyID: "AKIDTEST", SecretAccessKey: "secret"}, region: "us-east-1",
		endpoint: func(string) string { return server.URL + "/" }}
	got, err := discoverPrivateEgress(context.Background(), q, []netip.Addr{netip.MustParseAddr("172.31.38.10")})
	if err != nil {
		t.Fatal(err)
	}
	want := []PrivateEgressProposal{{Source: "rds-cluster:orders", Engine: "aurora-postgresql", Port: 5432, CIDRs: []string{"172.31.0.0/20"}, ExcludedCIDRs: []string{"10.42.0.0/24", "172.31.38.0/24"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestAWSIdentityWebIdentityAndIMDS(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	os.WriteFile(token, []byte("jwt\n"), 0o600)
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("Action") != "AssumeRoleWithWebIdentity" || r.Form.Get("WebIdentityToken") != "jwt" || r.Form.Get("RoleArn") != "arn:aws:iam::1:role/r" {
			http.Error(w, "bad", 400)
			return
		}
		w.Write([]byte(`<AssumeRoleWithWebIdentityResponse><AssumeRoleWithWebIdentityResult><Credentials>
<AccessKeyId>ASIAWEB</AccessKeyId><SecretAccessKey>s</SecretAccessKey><SessionToken>t</SessionToken>
</Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`))
	}))
	defer sts.Close()
	imds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
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
			w.Write([]byte(`{"AccessKeyId":"ASIAIMDS","SecretAccessKey":"s","Token":"t"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer imds.Close()
	env := func(values map[string]string) func(string) string { return func(k string) string { return values[k] } }

	web := awsIdentity{client: http.DefaultClient, imds: imds.URL, sts: sts.URL,
		getenv: env(map[string]string{"AWS_REGION": "us-east-2", "AWS_WEB_IDENTITY_TOKEN_FILE": token, "AWS_ROLE_ARN": "arn:aws:iam::1:role/r", "AWS_EC2_METADATA_DISABLED": "true"})}
	creds, region, err := web.resolve(context.Background(), "")
	if err != nil || creds.AccessKeyID != "ASIAWEB" || creds.SessionToken != "t" || region != "us-east-2" {
		t.Fatalf("web identity: %+v %q %v", creds, region, err)
	}
	instance := awsIdentity{client: http.DefaultClient, imds: imds.URL, getenv: env(nil)}
	creds, region, err = instance.resolve(context.Background(), "")
	if err != nil || creds.AccessKeyID != "ASIAIMDS" || region != "eu-west-1" {
		t.Fatalf("imds: %+v %q %v", creds, region, err)
	}
	disabled := awsIdentity{client: http.DefaultClient, imds: imds.URL, getenv: env(map[string]string{"AWS_EC2_METADATA_DISABLED": "true"})}
	if _, _, err := disabled.resolve(context.Background(), "us-east-1"); err == nil {
		t.Fatal("expected no identity")
	}
}

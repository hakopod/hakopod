package cluster

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/hakopod/hakopod/internal/awsquery"
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
	creds := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "AKIDTEST", SecretAccessKey: "secret"}, nil
	})
	q := awsquery.Client{HTTP: server.Client(), Credentials: creds, Region: "us-east-1", Endpoint: func(string) string { return server.URL + "/" }}
	got, err := discoverPrivateEgress(context.Background(), q, []netip.Addr{netip.MustParseAddr("172.31.38.10")})
	if err != nil {
		t.Fatal(err)
	}
	want := []PrivateEgressProposal{{Source: "rds-cluster:orders", Engine: "aurora-postgresql", Port: 5432, CIDRs: []string{"172.31.0.0/20"}, ExcludedCIDRs: []string{"10.42.0.0/24", "172.31.38.0/24"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

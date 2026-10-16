package cluster

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PrivateEgressProposal pre-fills an administrator form. It is never saved
// automatically and is not refreshed after an RDS failover.
type PrivateEgressProposal struct {
	Source        string   `json:"source"`
	Engine        string   `json:"engine"`
	Port          int32    `json:"port"`
	CIDRs         []string `json:"cidrs"`
	ExcludedCIDRs []string `json:"excluded_cidrs"`
}

// awsQuery calls AWS Query APIs over net/http with SigV4 from the core SDK
// module, so discovery adds no service SDK dependencies.
type awsQuery struct {
	client *http.Client
	creds  aws.Credentials
	region string
	// endpoint overrides https://<service>.<region>.amazonaws.com/ in tests.
	endpoint func(service string) string
}

func (q awsQuery) url(service string) string {
	if q.endpoint != nil {
		return q.endpoint(service)
	}
	return "https://" + service + "." + q.region + ".amazonaws.com/"
}

func (q awsQuery) call(ctx context.Context, service, action, version string, params url.Values, out any) error {
	params.Set("Action", action)
	params.Set("Version", version)
	body := params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, q.url(service), strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	sum := sha256.Sum256([]byte(body))
	if err := v4.NewSigner().SignHTTP(ctx, q.creds, req, hex.EncodeToString(sum[:]), service, q.region, time.Now()); err != nil {
		return err
	}
	return doXML(q.client, req, out)
}

func doXML(client *http.Client, req *http.Request, out any) error {
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", res.StatusCode)
	}
	return xml.Unmarshal(data, out)
}

// awsIdentity resolves credentials and region the way the installation runs:
// static environment keys, the projected web identity (hakopod-aws-identity),
// then the EC2 instance role over IMDSv2. Shared config files are not read.
type awsIdentity struct {
	client *http.Client
	getenv func(string) string
	imds   string // http://169.254.169.254 unless tested
	sts    string // https://sts.<region>.amazonaws.com/ unless tested
}

func (a awsIdentity) resolve(ctx context.Context, region string) (aws.Credentials, string, error) {
	imds := func() (string, error) { return "", errors.New("instance metadata disabled") }
	if !strings.EqualFold(a.getenv("AWS_EC2_METADATA_DISABLED"), "true") {
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
			region = a.getenv(name)
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
	if key, secret := a.getenv("AWS_ACCESS_KEY_ID"), a.getenv("AWS_SECRET_ACCESS_KEY"); key != "" && secret != "" {
		return aws.Credentials{AccessKeyID: key, SecretAccessKey: secret, SessionToken: a.getenv("AWS_SESSION_TOKEN"), Source: "environment"}, region, nil
	}
	if file, role := a.getenv("AWS_WEB_IDENTITY_TOKEN_FILE"), a.getenv("AWS_ROLE_ARN"); file != "" && role != "" {
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
	var role struct{ AccessKeyId, SecretAccessKey, Token string }
	if err := json.Unmarshal([]byte(raw), &role); err != nil || role.AccessKeyId == "" {
		return aws.Credentials{}, "", errors.New("invalid instance role credentials")
	}
	return aws.Credentials{AccessKeyID: role.AccessKeyId, SecretAccessKey: role.SecretAccessKey, SessionToken: role.Token, Source: "imds"}, region, nil
}

func (a awsIdentity) metadata(ctx context.Context, method, path, token string) (string, error) {
	base := a.imds
	if base == "" {
		base = "http://169.254.169.254"
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, base+path, nil)
	if err != nil {
		return "", err
	}
	if method == http.MethodPut {
		req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "300")
	} else {
		req.Header.Set("X-aws-ec2-metadata-token", token)
	}
	res, err := a.client.Do(req)
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

func (a awsIdentity) webIdentity(ctx context.Context, region, file, role string) (aws.Credentials, error) {
	token, err := os.ReadFile(file)
	if err != nil {
		return aws.Credentials{}, err
	}
	endpoint := a.sts
	if endpoint == "" {
		endpoint = "https://sts." + region + ".amazonaws.com/"
	}
	form := url.Values{"Action": {"AssumeRoleWithWebIdentity"}, "Version": {"2011-06-15"}, "RoleArn": {role},
		"RoleSessionName": {"hakopod-private-egress-discovery"}, "WebIdentityToken": {strings.TrimSpace(string(token))}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return aws.Credentials{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	var out struct {
		Credentials struct{ AccessKeyId, SecretAccessKey, SessionToken string } `xml:"AssumeRoleWithWebIdentityResult>Credentials"`
	}
	if err := doXML(a.client, req, &out); err != nil || out.Credentials.AccessKeyId == "" {
		return aws.Credentials{}, errors.New("sts:AssumeRoleWithWebIdentity failed")
	}
	c := out.Credentials
	return aws.Credentials{AccessKeyID: c.AccessKeyId, SecretAccessKey: c.SecretAccessKey, SessionToken: c.SessionToken, Source: "web-identity"}, nil
}

// DiscoverPrivateEgress uses the installation's AWS identity. It needs
// rds:DescribeDBInstances and ec2:DescribeSubnets.
func (c *Client) DiscoverPrivateEgress(ctx context.Context, region string) ([]PrivateEgressProposal, error) {
	if c.CloudMode() || c.options.WorkloadPolicy != nil {
		return nil, fmt.Errorf("%w: private egress discovery requires a self-hosted installation", ErrCloudLimit)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	creds, region, err := awsIdentity{client: client, getenv: os.Getenv}.resolve(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("private egress discovery: no AWS identity or region is available to the API")
	}
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 200})
	if err != nil {
		return nil, fmt.Errorf("list Kubernetes nodes: %w", err)
	}
	addresses := []netip.Addr{}
	for _, node := range nodes.Items {
		for _, address := range node.Status.Addresses {
			if address.Type != corev1.NodeInternalIP && address.Type != corev1.NodeExternalIP {
				continue
			}
			if addr, err := netip.ParseAddr(address.Address); err == nil {
				addresses = append(addresses, addr)
			}
		}
	}
	return discoverPrivateEgress(ctx, awsQuery{client: client, creds: creds, region: region}, addresses)
}

type rdsInstancesPage struct {
	Instances []struct {
		ID      string   `xml:"DBInstanceIdentifier"`
		Cluster string   `xml:"DBClusterIdentifier"`
		Engine  string   `xml:"Engine"`
		Port    *int32   `xml:"Endpoint>Port"`
		Subnets []string `xml:"DBSubnetGroup>Subnets>Subnet>SubnetIdentifier"`
	} `xml:"DescribeDBInstancesResult>DBInstances>DBInstance"`
	Marker string `xml:"DescribeDBInstancesResult>Marker"`
}

// discoverPrivateEgress groups instances by Aurora cluster (or instance) and
// proposes their DB subnet group CIDRs, minus subnets holding a cluster node
// (incident case: the node's 172.31.38.0/24) or failing binding validation.
func discoverPrivateEgress(ctx context.Context, q awsQuery, nodes []netip.Addr) ([]PrivateEgressProposal, error) {
	type group struct {
		engine  string
		port    int32
		subnets map[string]bool
	}
	groups := map[string]*group{}
	subnetIDs := map[string]bool{}
	marker := ""
	for pages := 0; ; pages++ {
		if pages >= 5 {
			return nil, fmt.Errorf("private egress discovery: more than 500 RDS instances")
		}
		params := url.Values{"MaxRecords": {"100"}}
		if marker != "" {
			params.Set("Marker", marker)
		}
		var page rdsInstancesPage
		if err := q.call(ctx, "rds", "DescribeDBInstances", "2014-10-31", params, &page); err != nil {
			return nil, fmt.Errorf("private egress discovery: rds:DescribeDBInstances failed")
		}
		for _, instance := range page.Instances {
			if instance.Port == nil || len(instance.Subnets) == 0 {
				continue
			}
			source := "rds-instance:" + instance.ID
			if instance.Cluster != "" {
				source = "rds-cluster:" + instance.Cluster
			}
			g := groups[source]
			if g == nil {
				g = &group{engine: instance.Engine, port: *instance.Port, subnets: map[string]bool{}}
				groups[source] = g
			}
			for _, id := range instance.Subnets {
				if id != "" {
					g.subnets[id], subnetIDs[id] = true, true
				}
			}
		}
		if marker = page.Marker; marker == "" {
			break
		}
	}
	cidrs := map[string]string{}
	ids := make([]string, 0, len(subnetIDs))
	for id := range subnetIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for start := 0; start < len(ids); start += 200 {
		end := min(start+200, len(ids))
		params := url.Values{}
		for i, id := range ids[start:end] {
			params.Set("SubnetId."+strconv.Itoa(i+1), id)
		}
		var out struct {
			Subnets []struct {
				ID   string `xml:"subnetId"`
				CIDR string `xml:"cidrBlock"`
			} `xml:"subnetSet>item"`
		}
		if err := q.call(ctx, "ec2", "DescribeSubnets", "2016-11-15", params, &out); err != nil {
			return nil, fmt.Errorf("private egress discovery: ec2:DescribeSubnets failed")
		}
		for _, subnet := range out.Subnets {
			cidrs[subnet.ID] = subnet.CIDR
		}
	}
	result := []PrivateEgressProposal{}
	for source, g := range groups {
		p := PrivateEgressProposal{Source: source, Engine: g.engine, Port: g.port, CIDRs: []string{}, ExcludedCIDRs: []string{}}
		for id := range g.subnets {
			cidr := cidrs[id]
			if cidr == "" {
				continue
			}
			if usablePrivateEgressCIDR(cidr, nodes) {
				p.CIDRs = append(p.CIDRs, cidr)
			} else {
				p.ExcludedCIDRs = append(p.ExcludedCIDRs, cidr)
			}
		}
		sort.Strings(p.CIDRs)
		sort.Strings(p.ExcludedCIDRs)
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Source < result[j].Source })
	return result, nil
}

func usablePrivateEgressCIDR(cidr string, nodes []netip.Addr) bool {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return false
	}
	for _, node := range nodes {
		if prefix.Contains(node) {
			return false
		}
	}
	probe := PrivateEgressBinding{Name: "probe", Project: "p", Environment: "e", Application: "a", Services: []string{"s"}, CIDRs: []string{cidr}, Ports: []int32{1}}
	return ValidatePrivateEgressBindings([]PrivateEgressBinding{probe}) == nil
}

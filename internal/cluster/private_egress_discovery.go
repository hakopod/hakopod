package cluster

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/hakopod/hakopod/internal/awsquery"
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

// DiscoverPrivateEgress uses the installation's AWS identity. It needs
// rds:DescribeDBInstances and ec2:DescribeSubnets.
func (c *Client) DiscoverPrivateEgress(ctx context.Context, region string) ([]PrivateEgressProposal, error) {
	if c.CloudMode() || c.options.WorkloadPolicy != nil {
		return nil, fmt.Errorf("%w: private egress discovery requires a self-hosted installation", ErrCloudLimit)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	creds, region, err := awsquery.Identity{HTTP: client, SessionName: "hakopod-private-egress-discovery"}.Resolve(ctx, region)
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
	return discoverPrivateEgress(ctx, awsquery.Client{HTTP: client, Region: region,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) { return creds, nil })}, addresses)
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
func discoverPrivateEgress(ctx context.Context, q awsquery.Client, nodes []netip.Addr) ([]PrivateEgressProposal, error) {
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
		if err := q.Call(ctx, "rds", "DescribeDBInstances", "2014-10-31", params, &page); err != nil {
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
		if err := q.Call(ctx, "ec2", "DescribeSubnets", "2016-11-15", params, &out); err != nil {
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

package cluster

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/rds"
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

type rdsInstancesAPI interface {
	DescribeDBInstances(context.Context, *rds.DescribeDBInstancesInput, ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error)
}

type ec2SubnetsAPI interface {
	DescribeSubnets(context.Context, *ec2.DescribeSubnetsInput, ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
}

// DiscoverPrivateEgress uses the installation's default AWS identity. It needs
// rds:DescribeDBInstances and ec2:DescribeSubnets.
func (c *Client) DiscoverPrivateEgress(ctx context.Context, region string) ([]PrivateEgressProposal, error) {
	if c.CloudMode() || c.options.WorkloadPolicy != nil {
		return nil, fmt.Errorf("%w: private egress discovery requires a self-hosted installation", ErrCloudLimit)
	}
	options := []func(*config.LoadOptions) error{}
	if region != "" {
		options = append(options, config.WithRegion(region))
	}
	cfg, err := config.LoadDefaultConfig(ctx, options...)
	if err == nil && cfg.Region == "" {
		err = errors.New("no region")
	}
	if err == nil {
		_, err = cfg.Credentials.Retrieve(ctx)
	}
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
	return discoverPrivateEgress(ctx, rds.NewFromConfig(cfg), ec2.NewFromConfig(cfg), addresses)
}

// discoverPrivateEgress groups instances by Aurora cluster (or instance) and
// proposes their DB subnet group CIDRs, minus subnets holding a cluster node
// (incident case: the node's 172.31.38.0/24) or failing binding validation.
func discoverPrivateEgress(ctx context.Context, db rdsInstancesAPI, network ec2SubnetsAPI, nodes []netip.Addr) ([]PrivateEgressProposal, error) {
	type group struct {
		engine  string
		port    int32
		subnets map[string]bool
	}
	groups := map[string]*group{}
	subnetIDs := map[string]bool{}
	paginator := rds.NewDescribeDBInstancesPaginator(db, &rds.DescribeDBInstancesInput{MaxRecords: aws.Int32(100)})
	for pages := 0; paginator.HasMorePages(); pages++ {
		if pages >= 5 {
			return nil, fmt.Errorf("private egress discovery: more than 500 RDS instances")
		}
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("private egress discovery: rds:DescribeDBInstances failed")
		}
		for _, instance := range page.DBInstances {
			if instance.Endpoint == nil || instance.DBSubnetGroup == nil {
				continue
			}
			source := "rds-instance:" + aws.ToString(instance.DBInstanceIdentifier)
			if instance.DBClusterIdentifier != nil {
				source = "rds-cluster:" + aws.ToString(instance.DBClusterIdentifier)
			}
			g := groups[source]
			if g == nil {
				g = &group{engine: aws.ToString(instance.Engine), port: aws.ToInt32(instance.Endpoint.Port), subnets: map[string]bool{}}
				groups[source] = g
			}
			for _, subnet := range instance.DBSubnetGroup.Subnets {
				if id := aws.ToString(subnet.SubnetIdentifier); id != "" {
					g.subnets[id], subnetIDs[id] = true, true
				}
			}
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
		out, err := network.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: ids[start:end]})
		if err != nil {
			return nil, fmt.Errorf("private egress discovery: ec2:DescribeSubnets failed")
		}
		for _, subnet := range out.Subnets {
			cidrs[aws.ToString(subnet.SubnetId)] = aws.ToString(subnet.CidrBlock)
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

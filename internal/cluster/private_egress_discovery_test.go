package cluster

import (
	"context"
	"net/netip"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
)

type fakeRDS struct{ out *rds.DescribeDBInstancesOutput }

func (f fakeRDS) DescribeDBInstances(context.Context, *rds.DescribeDBInstancesInput, ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	return f.out, nil
}

type fakeEC2 map[string]string

func (f fakeEC2) DescribeSubnets(_ context.Context, in *ec2.DescribeSubnetsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	out := &ec2.DescribeSubnetsOutput{}
	for _, id := range in.SubnetIds {
		out.Subnets = append(out.Subnets, ec2types.Subnet{SubnetId: aws.String(id), CidrBlock: aws.String(f[id])})
	}
	return out, nil
}

func TestDiscoverPrivateEgressExcludesNodeSubnets(t *testing.T) {
	group := &rdstypes.DBSubnetGroup{Subnets: []rdstypes.Subnet{{SubnetIdentifier: aws.String("a")}, {SubnetIdentifier: aws.String("b")}, {SubnetIdentifier: aws.String("c")}}}
	instance := func(id string) rdstypes.DBInstance {
		return rdstypes.DBInstance{DBInstanceIdentifier: aws.String(id), DBClusterIdentifier: aws.String("orders"), Engine: aws.String("aurora-postgresql"), Endpoint: &rdstypes.Endpoint{Port: aws.Int32(5432)}, DBSubnetGroup: group}
	}
	db := fakeRDS{&rds.DescribeDBInstancesOutput{DBInstances: []rdstypes.DBInstance{instance("orders-1"), instance("orders-2")}}}
	subnets := fakeEC2{"a": "172.31.0.0/20", "b": "172.31.38.0/24", "c": "10.42.0.0/24"}
	got, err := discoverPrivateEgress(context.Background(), db, subnets, []netip.Addr{netip.MustParseAddr("172.31.38.10")})
	if err != nil {
		t.Fatal(err)
	}
	want := []PrivateEgressProposal{{Source: "rds-cluster:orders", Engine: "aurora-postgresql", Port: 5432, CIDRs: []string{"172.31.0.0/20"}, ExcludedCIDRs: []string{"10.42.0.0/24", "172.31.38.0/24"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

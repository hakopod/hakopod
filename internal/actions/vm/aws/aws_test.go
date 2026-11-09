package aws

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"github.com/hakopod/hakopod/internal/actions/vm"
)

type fakeEC2 struct {
	template   *ec2.CreateLaunchTemplateInput
	fleet      *ec2.CreateFleetInput
	deleted    []string
	terminated []string
	instances  map[string]types.Instance
	fleetOut   *ec2.CreateFleetOutput
}

func (f *fakeEC2) CreateLaunchTemplate(_ context.Context, in *ec2.CreateLaunchTemplateInput, _ ...func(*ec2.Options)) (*ec2.CreateLaunchTemplateOutput, error) {
	f.template = in
	return &ec2.CreateLaunchTemplateOutput{LaunchTemplate: &types.LaunchTemplate{LaunchTemplateId: sdk.String("lt-1")}}, nil
}
func (f *fakeEC2) DeleteLaunchTemplate(_ context.Context, in *ec2.DeleteLaunchTemplateInput, _ ...func(*ec2.Options)) (*ec2.DeleteLaunchTemplateOutput, error) {
	f.deleted = append(f.deleted, *in.LaunchTemplateId)
	return &ec2.DeleteLaunchTemplateOutput{}, nil
}
func (f *fakeEC2) CreateFleet(_ context.Context, in *ec2.CreateFleetInput, _ ...func(*ec2.Options)) (*ec2.CreateFleetOutput, error) {
	f.fleet = in
	return f.fleetOut, nil
}
func (f *fakeEC2) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	out := &ec2.DescribeInstancesOutput{}
	for _, id := range in.InstanceIds {
		i, ok := f.instances[id]
		if !ok {
			return nil, &smithy.GenericAPIError{Code: "InvalidInstanceID.NotFound"}
		}
		out.Reservations = append(out.Reservations, types.Reservation{Instances: []types.Instance{i}})
	}
	if len(in.InstanceIds) == 0 {
		for _, i := range f.instances {
			out.Reservations = append(out.Reservations, types.Reservation{Instances: []types.Instance{i}})
		}
	}
	return out, nil
}
func (f *fakeEC2) TerminateInstances(_ context.Context, in *ec2.TerminateInstancesInput, _ ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	f.terminated = append(f.terminated, in.InstanceIds...)
	return &ec2.TerminateInstancesOutput{}, nil
}
func (f *fakeEC2) DescribeSubnets(context.Context, *ec2.DescribeSubnetsInput, ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	return &ec2.DescribeSubnetsOutput{}, nil
}
func (f *fakeEC2) DescribeSecurityGroups(context.Context, *ec2.DescribeSecurityGroupsInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []types.SecurityGroup{{GroupId: sdk.String("sg-1"), IpPermissions: []types.IpPermission{{}}}, {GroupId: sdk.String("sg-2")}}}, nil
}

func tagged(id, installation string, state types.InstanceStateName, reason string) types.Instance {
	i := types.Instance{InstanceId: sdk.String(id), InstanceType: "c7i.xlarge", InstanceLifecycle: types.InstanceLifecycleTypeSpot,
		State: &types.InstanceState{Name: state}, Tags: []types.Tag{{Key: sdk.String(vm.TagInstallation), Value: sdk.String(installation)}}}
	if reason != "" {
		i.StateReason = &types.StateReason{Code: sdk.String(reason)}
	}
	return i
}

func testClient(f *fakeEC2) *Client {
	return &Client{ec2: f, config: Config{Installation: "inst", Region: "eu-central-1", SubnetIDs: []string{"subnet-a", "subnet-b"}, SecurityGroupIDs: []string{"sg-1"}, Architecture: "arm64", DiskGiB: 100}}
}

func TestLaunch(t *testing.T) {
	f := &fakeEC2{fleetOut: &ec2.CreateFleetOutput{Instances: []types.CreateFleetInstance{{InstanceIds: []string{"i-1"}, InstanceType: "c7g.xlarge", Lifecycle: types.InstanceLifecycleSpot}}}}
	got, err := testClient(f).Launch(context.Background(), vm.LaunchInput{Name: "slot1", Tags: map[string]string{vm.TagSlot: "slot1"}, InstanceTypes: []string{"c7g.xlarge", "m7g.xlarge"}, Spot: true, UserData: "#!/bin/bash\n"})
	if err != nil || got.ID != "i-1" || got.Lifecycle != "spot" || got.Type != "c7g.xlarge" {
		t.Fatal(got, err)
	}
	data := f.template.LaunchTemplateData
	if *data.ImageId != "resolve:ssm:/aws/service/canonical/ubuntu/server/24.04/stable/current/arm64/hvm/ebs-gp3/ami-id" {
		t.Fatal(*data.ImageId)
	}
	if data.InstanceInitiatedShutdownBehavior != types.ShutdownBehaviorTerminate || data.MetadataOptions.HttpTokens != types.LaunchTemplateHttpTokensStateRequired || *data.MetadataOptions.HttpPutResponseHopLimit != 1 {
		t.Fatal("shutdown or IMDSv2 settings", data)
	}
	ebs := data.BlockDeviceMappings[0].Ebs
	if !*ebs.Encrypted || *ebs.VolumeSize != 100 || !*ebs.DeleteOnTermination {
		t.Fatal("root volume", ebs)
	}
	if decoded, _ := base64.StdEncoding.DecodeString(*data.UserData); string(decoded) != "#!/bin/bash\n" {
		t.Fatal("user data", string(decoded))
	}
	tags := map[string]string{}
	for _, tag := range data.TagSpecifications[0].Tags {
		tags[*tag.Key] = *tag.Value
	}
	if tags[vm.TagInstallation] != "inst" || tags[vm.TagSlot] != "slot1" || data.TagSpecifications[0].ResourceType != types.ResourceTypeInstance {
		t.Fatal("instance tags", tags)
	}
	if !strings.HasPrefix(*f.template.LaunchTemplateName, "hakopod-slot1-") {
		t.Fatal(*f.template.LaunchTemplateName)
	}
	if f.fleet.Type != types.FleetTypeInstant || f.fleet.TargetCapacitySpecification.DefaultTargetCapacityType != types.DefaultTargetCapacityTypeSpot ||
		f.fleet.SpotOptions.AllocationStrategy != types.SpotAllocationStrategyPriceCapacityOptimized || len(f.fleet.LaunchTemplateConfigs[0].Overrides) != 4 {
		t.Fatal("fleet request", f.fleet)
	}
	if len(f.deleted) != 1 {
		t.Fatal("the launch template with the runner configuration was kept")
	}

	f = &fakeEC2{fleetOut: &ec2.CreateFleetOutput{Errors: []types.CreateFleetError{{ErrorCode: sdk.String("InsufficientInstanceCapacity")}}}}
	if _, err = testClient(f).Launch(context.Background(), vm.LaunchInput{Name: "slot2", InstanceTypes: []string{"c7g.xlarge"}}); !errors.Is(err, vm.ErrNoCapacity) || f.fleet.TargetCapacitySpecification.DefaultTargetCapacityType != types.DefaultTargetCapacityTypeOnDemand || len(f.deleted) != 1 {
		t.Fatal("failed on-demand launch", err)
	}
}

func TestStateTerminateAndCheck(t *testing.T) {
	f := &fakeEC2{instances: map[string]types.Instance{
		"i-ours":    tagged("i-ours", "inst", types.InstanceStateNameTerminated, "Server.SpotInstanceTermination"),
		"i-running": tagged("i-running", "inst", types.InstanceStateNameRunning, ""),
		"i-other":   tagged("i-other", "someone-else", types.InstanceStateNameRunning, ""),
	}}
	c := testClient(f)
	ctx := context.Background()
	got, err := c.State(ctx, "i-ours")
	if err != nil || !got.Interrupted() || !got.Gone() || got.Lifecycle != "spot" {
		t.Fatal(got, err)
	}
	if _, err = c.State(ctx, "i-missing"); !errors.Is(err, vm.ErrNotFound) {
		t.Fatal(err)
	}
	if err = c.Terminate(ctx, "i-other"); err == nil {
		t.Fatal("terminated an instance of another installation")
	}
	if err = c.Terminate(ctx, "i-ours"); err != nil {
		t.Fatal(err)
	}
	if err = c.Terminate(ctx, "i-running"); err != nil || len(f.terminated) != 1 || f.terminated[0] != "i-running" {
		t.Fatal(f.terminated, err)
	}
	warnings, err := c.Check(ctx)
	if err != nil || len(warnings) != 1 {
		t.Fatal(warnings, err)
	}
}

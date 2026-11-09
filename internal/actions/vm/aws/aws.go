// Package aws launches single-use Actions runner VMs with an EC2 instant fleet
// in the customer's account. Hakopod's own AWS identity assumes the customer's
// role with an external ID; no static keys are configured here.
package aws

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	"github.com/hakopod/hakopod/internal/actions/vm"
)

type Config struct {
	Installation     string
	Region           string
	RoleARN          string
	ExternalID       string
	SubnetIDs        []string
	SecurityGroupIDs []string
	Architecture     string
	DiskGiB          int64
}

type ec2API interface {
	CreateLaunchTemplate(context.Context, *ec2.CreateLaunchTemplateInput, ...func(*ec2.Options)) (*ec2.CreateLaunchTemplateOutput, error)
	DeleteLaunchTemplate(context.Context, *ec2.DeleteLaunchTemplateInput, ...func(*ec2.Options)) (*ec2.DeleteLaunchTemplateOutput, error)
	CreateFleet(context.Context, *ec2.CreateFleetInput, ...func(*ec2.Options)) (*ec2.CreateFleetOutput, error)
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	TerminateInstances(context.Context, *ec2.TerminateInstancesInput, ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error)
	DescribeSubnets(context.Context, *ec2.DescribeSubnetsInput, ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
	DescribeSecurityGroups(context.Context, *ec2.DescribeSecurityGroupsInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
}

type Client struct {
	ec2    ec2API
	config Config
}

// New assumes the capacity's role from Hakopod's default AWS credential chain
// (environment, web identity, instance or pod role). Credentials are cached
// and refreshed by the SDK.
func New(ctx context.Context, c Config) (*Client, error) {
	if c.Installation == "" || c.ExternalID == "" {
		return nil, errors.New("an installation and external ID are required")
	}
	base, err := config.LoadDefaultConfig(ctx, config.WithRegion(c.Region))
	if err != nil {
		return nil, fmt.Errorf("AWS configuration: %w", err)
	}
	role := stscreds.NewAssumeRoleProvider(sts.NewFromConfig(base), c.RoleARN, func(o *stscreds.AssumeRoleOptions) {
		o.ExternalID = sdk.String(c.ExternalID)
		o.RoleSessionName = "hakopod-actions"
	})
	base.Credentials = sdk.NewCredentialsCache(role)
	return &Client{ec2: ec2.NewFromConfig(base), config: c}, nil
}

// Canonical publishes the current Ubuntu 24.04 image for each architecture as
// a public SSM parameter. Instant fleets resolve it at launch.
func ubuntuImage(architecture string) string {
	return "resolve:ssm:/aws/service/canonical/ubuntu/server/24.04/stable/current/" + architecture + "/hvm/ebs-gp3/ami-id"
}

func tagSpec(resource types.ResourceType, tags map[string]string) types.LaunchTemplateTagSpecificationRequest {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	spec := types.LaunchTemplateTagSpecificationRequest{ResourceType: resource}
	for _, k := range keys {
		spec.Tags = append(spec.Tags, types.Tag{Key: sdk.String(k), Value: sdk.String(tags[k])})
	}
	return spec
}

// Launch creates a throwaway launch template carrying the boot script, starts
// one instance from it with an instant fleet, and deletes the template. There
// is no client token: a failed fleet would replay its failure for that token.
// Callers look for an instance tagged with the slot before launching again.
func (c *Client) Launch(ctx context.Context, in vm.LaunchInput) (vm.Instance, error) {
	tags := map[string]string{vm.TagInstallation: c.config.Installation}
	for k, v := range in.Tags {
		tags[k] = v
	}
	if len(in.InstanceTypes) == 0 || in.Name == "" {
		return vm.Instance{}, errors.New("a launch needs instance types and a name")
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return vm.Instance{}, err
	}
	template, err := c.ec2.CreateLaunchTemplate(ctx, &ec2.CreateLaunchTemplateInput{
		LaunchTemplateName: sdk.String("hakopod-" + in.Name + "-" + hex.EncodeToString(suffix)),
		TagSpecifications:  []types.TagSpecification{{ResourceType: types.ResourceTypeLaunchTemplate, Tags: tagSpec("", tags).Tags}},
		LaunchTemplateData: &types.RequestLaunchTemplateData{
			ImageId:                           sdk.String(ubuntuImage(c.config.Architecture)),
			UserData:                          sdk.String(base64.StdEncoding.EncodeToString([]byte(in.UserData))),
			InstanceInitiatedShutdownBehavior: types.ShutdownBehaviorTerminate,
			SecurityGroupIds:                  c.config.SecurityGroupIDs,
			MetadataOptions: &types.LaunchTemplateInstanceMetadataOptionsRequest{
				HttpEndpoint:            types.LaunchTemplateInstanceMetadataEndpointStateEnabled,
				HttpTokens:              types.LaunchTemplateHttpTokensStateRequired,
				HttpPutResponseHopLimit: sdk.Int32(1),
			},
			BlockDeviceMappings: []types.LaunchTemplateBlockDeviceMappingRequest{{
				DeviceName: sdk.String("/dev/sda1"),
				Ebs: &types.LaunchTemplateEbsBlockDeviceRequest{
					VolumeSize: sdk.Int32(int32(c.config.DiskGiB)), VolumeType: types.VolumeTypeGp3,
					Encrypted: sdk.Bool(true), DeleteOnTermination: sdk.Bool(true),
				},
			}},
			TagSpecifications: []types.LaunchTemplateTagSpecificationRequest{tagSpec(types.ResourceTypeInstance, tags), tagSpec(types.ResourceTypeVolume, tags)},
		},
	})
	if err != nil {
		return vm.Instance{}, awsError("create the launch template", err)
	}
	id := template.LaunchTemplate.LaunchTemplateId
	defer func() {
		// The instance no longer needs its template, and the template holds
		// the single-use runner configuration in its user data.
		_, _ = c.ec2.DeleteLaunchTemplate(context.WithoutCancel(ctx), &ec2.DeleteLaunchTemplateInput{LaunchTemplateId: id})
	}()
	overrides := []types.FleetLaunchTemplateOverridesRequest{}
	for _, t := range in.InstanceTypes {
		for _, subnet := range c.config.SubnetIDs {
			overrides = append(overrides, types.FleetLaunchTemplateOverridesRequest{InstanceType: types.InstanceType(t), SubnetId: sdk.String(subnet)})
		}
	}
	capacity := types.DefaultTargetCapacityTypeOnDemand
	if in.Spot {
		capacity = types.DefaultTargetCapacityTypeSpot
	}
	fleet, err := c.ec2.CreateFleet(ctx, &ec2.CreateFleetInput{
		Type: types.FleetTypeInstant,
		LaunchTemplateConfigs: []types.FleetLaunchTemplateConfigRequest{{
			LaunchTemplateSpecification: &types.FleetLaunchTemplateSpecificationRequest{LaunchTemplateId: id, Version: sdk.String("$Latest")},
			Overrides:                   overrides,
		}},
		TargetCapacitySpecification: &types.TargetCapacitySpecificationRequest{TotalTargetCapacity: sdk.Int32(1), DefaultTargetCapacityType: capacity},
		SpotOptions:                 &types.SpotOptionsRequest{AllocationStrategy: types.SpotAllocationStrategyPriceCapacityOptimized},
		OnDemandOptions:             &types.OnDemandOptionsRequest{AllocationStrategy: types.FleetOnDemandAllocationStrategyLowestPrice},
	})
	if err != nil {
		return vm.Instance{}, awsError("create the fleet", err)
	}
	for _, launched := range fleet.Instances {
		if len(launched.InstanceIds) > 0 {
			return vm.Instance{ID: launched.InstanceIds[0], Type: string(launched.InstanceType), Lifecycle: lifecycle(launched.Lifecycle), State: "pending", Tags: tags}, nil
		}
	}
	codes := []string{}
	for _, e := range fleet.Errors {
		if e.ErrorCode != nil && !slices.Contains(codes, *e.ErrorCode) {
			codes = append(codes, *e.ErrorCode)
		}
	}
	return vm.Instance{}, fmt.Errorf("%w (%s)", vm.ErrNoCapacity, strings.Join(codes, ", "))
}

func lifecycle(l types.InstanceLifecycle) string {
	if l == types.InstanceLifecycleSpot {
		return "spot"
	}
	return "on-demand"
}

func instance(i types.Instance) vm.Instance {
	out := vm.Instance{ID: sdk.ToString(i.InstanceId), Type: string(i.InstanceType), Lifecycle: "on-demand", Tags: map[string]string{}}
	if i.InstanceLifecycle == types.InstanceLifecycleTypeSpot {
		out.Lifecycle = "spot"
	}
	if i.State != nil {
		out.State = string(i.State.Name)
	}
	if i.StateReason != nil {
		out.StateReason = sdk.ToString(i.StateReason.Code)
	}
	if i.LaunchTime != nil {
		out.LaunchedAt = *i.LaunchTime
	}
	for _, t := range i.Tags {
		out.Tags[sdk.ToString(t.Key)] = sdk.ToString(t.Value)
	}
	return out
}

func (c *Client) State(ctx context.Context, id string) (vm.Instance, error) {
	out, err := c.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}})
	var api smithy.APIError
	if errors.As(err, &api) && api.ErrorCode() == "InvalidInstanceID.NotFound" {
		return vm.Instance{}, vm.ErrNotFound
	}
	if err != nil {
		return vm.Instance{}, awsError("describe the instance", err)
	}
	for _, r := range out.Reservations {
		for _, i := range r.Instances {
			if sdk.ToString(i.InstanceId) == id {
				return instance(i), nil
			}
		}
	}
	return vm.Instance{}, vm.ErrNotFound
}

// List returns instances carrying every given tag and this installation's.
// Terminated instances stay listed for about an hour, with their reason.
func (c *Client) List(ctx context.Context, tags map[string]string) ([]vm.Instance, error) {
	filters := []types.Filter{{Name: sdk.String("tag:" + vm.TagInstallation), Values: []string{c.config.Installation}}}
	for k, v := range tags {
		filters = append(filters, types.Filter{Name: sdk.String("tag:" + k), Values: []string{v}})
	}
	input := &ec2.DescribeInstancesInput{Filters: filters, MaxResults: sdk.Int32(1000)}
	out := []vm.Instance{}
	for page := 0; page < 10; page++ {
		result, err := c.ec2.DescribeInstances(ctx, input)
		if err != nil {
			return nil, awsError("list instances", err)
		}
		for _, r := range result.Reservations {
			for _, i := range r.Instances {
				out = append(out, instance(i))
			}
		}
		if result.NextToken == nil {
			return out, nil
		}
		input.NextToken = result.NextToken
	}
	return nil, errors.New("more than 10,000 tagged instances; refusing to act on a partial list")
}

// Terminate rechecks the installation tag first, so a recorded ID can never
// terminate an instance Hakopod did not launch. The documented IAM policy
// enforces the same condition on the AWS side.
func (c *Client) Terminate(ctx context.Context, id string) error {
	current, err := c.State(ctx, id)
	if errors.Is(err, vm.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.Tags[vm.TagInstallation] != c.config.Installation {
		return fmt.Errorf("instance %s is not tagged for this installation; not terminating it", id)
	}
	if current.State == "terminated" {
		return nil
	}
	_, err = c.ec2.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{id}})
	return awsError("terminate the instance", err)
}

// Check assumes the role and reads the configured subnets and security
// groups. Inbound rules are reported: a runner needs none.
func (c *Client) Check(ctx context.Context) ([]string, error) {
	if _, err := c.ec2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: c.config.SubnetIDs}); err != nil {
		return nil, awsError("read the subnets", err)
	}
	groups, err := c.ec2.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: c.config.SecurityGroupIDs})
	if err != nil {
		return nil, awsError("read the security groups", err)
	}
	warnings := []string{}
	for _, g := range groups.SecurityGroups {
		if len(g.IpPermissions) > 0 {
			warnings = append(warnings, fmt.Sprintf("security group %s allows inbound traffic; runners need none", sdk.ToString(g.GroupId)))
		}
	}
	return warnings, nil
}

// awsError keeps the AWS error code and drops the message, which can echo
// request details.
func awsError(action string, err error) error {
	if err == nil {
		return nil
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		return fmt.Errorf("AWS could not %s: %s", action, api.ErrorCode())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("AWS could not %s; check that Hakopod can assume the capacity's role", action)
}

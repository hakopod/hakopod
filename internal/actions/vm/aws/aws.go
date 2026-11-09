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
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/actions/vm"
	"github.com/hakopod/hakopod/internal/awsquery"
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

// Client speaks the EC2 Query API directly (internal/awsquery), so the EC2
// and STS SDK modules stay out of go.mod.
type Client struct {
	ec2    awsquery.Client
	config Config
}

// New assumes the capacity's role from Hakopod's own AWS identity:
// environment keys, a web identity token, or the EC2 instance role. Shared
// config files and SSO profiles are not read. The role's credentials are
// cached and refreshed before they expire.
func New(_ context.Context, c Config) (*Client, error) {
	if c.Installation == "" || c.ExternalID == "" || c.Region == "" {
		return nil, errors.New("an installation, region and external ID are required")
	}
	base := awsquery.Client{Region: c.Region, Credentials: awsquery.Identity{SessionName: "hakopod-actions"}.Provider(c.Region)}
	role := base
	role.Credentials = awsquery.AssumeRole(base, c.RoleARN, c.ExternalID, "hakopod-actions")
	return &Client{ec2: role, config: c}, nil
}

func (c *Client) call(ctx context.Context, action string, params url.Values, out any) error {
	return c.ec2.Call(ctx, "ec2", action, "2016-11-15", params, out)
}

// list sets EC2's flattened list parameters: prefix.1, prefix.2, ...
func list(p url.Values, prefix string, values []string) {
	for i, v := range values {
		p.Set(prefix+"."+strconv.Itoa(i+1), v)
	}
}

func tagSpec(p url.Values, prefix, resource string, tags map[string]string) {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	p.Set(prefix+".ResourceType", resource)
	for i, k := range keys {
		p.Set(fmt.Sprintf("%s.Tag.%d.Key", prefix, i+1), k)
		p.Set(fmt.Sprintf("%s.Tag.%d.Value", prefix, i+1), tags[k])
	}
}

// Canonical publishes the current Ubuntu 24.04 image for each architecture as
// a public SSM parameter. Instant fleets resolve it at launch.
func ubuntuImage(architecture string) string {
	return "resolve:ssm:/aws/service/canonical/ubuntu/server/24.04/stable/current/" + architecture + "/hvm/ebs-gp3/ami-id"
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
	p := url.Values{"LaunchTemplateName": {"hakopod-" + in.Name + "-" + hex.EncodeToString(suffix)}}
	tagSpec(p, "TagSpecification.1", "launch-template", tags)
	d := "LaunchTemplateData."
	image, shutdown, disk := in.Image, "terminate", in.DiskGiB
	if image == "" {
		image = ubuntuImage(c.config.Architecture)
	}
	if in.StopOnShutdown {
		shutdown = "stop"
	}
	if disk == 0 {
		disk = c.config.DiskGiB
	}
	p.Set(d+"ImageId", image)
	p.Set(d+"UserData", base64.StdEncoding.EncodeToString([]byte(in.UserData)))
	p.Set(d+"InstanceInitiatedShutdownBehavior", shutdown)
	list(p, d+"SecurityGroupId", c.config.SecurityGroupIDs)
	p.Set(d+"MetadataOptions.HttpEndpoint", "enabled")
	p.Set(d+"MetadataOptions.HttpTokens", "required")
	p.Set(d+"MetadataOptions.HttpPutResponseHopLimit", "1")
	p.Set(d+"BlockDeviceMapping.1.DeviceName", "/dev/sda1")
	p.Set(d+"BlockDeviceMapping.1.Ebs.VolumeSize", strconv.FormatInt(disk, 10))
	p.Set(d+"BlockDeviceMapping.1.Ebs.VolumeType", "gp3")
	p.Set(d+"BlockDeviceMapping.1.Ebs.Encrypted", "true")
	p.Set(d+"BlockDeviceMapping.1.Ebs.DeleteOnTermination", "true")
	if in.VolumeInitRate > 0 {
		p.Set(d+"BlockDeviceMapping.1.Ebs.VolumeInitializationRate", strconv.FormatInt(in.VolumeInitRate, 10))
	}
	tagSpec(p, d+"TagSpecification.1", "instance", tags)
	tagSpec(p, d+"TagSpecification.2", "volume", tags)
	var template struct {
		ID string `xml:"launchTemplate>launchTemplateId"`
	}
	if err := c.call(ctx, "CreateLaunchTemplate", p, &template); err != nil {
		return vm.Instance{}, awsError("create the launch template", err)
	}
	if template.ID == "" {
		return vm.Instance{}, errors.New("AWS created a launch template without an ID")
	}
	defer func() {
		// The instance no longer needs its template, and the template holds
		// the single-use runner configuration in its user data.
		_ = c.call(context.WithoutCancel(ctx), "DeleteLaunchTemplate", url.Values{"LaunchTemplateId": {template.ID}}, &struct{}{})
	}()
	f := "LaunchTemplateConfigs.1."
	p = url.Values{"Type": {"instant"}, f + "LaunchTemplateSpecification.LaunchTemplateId": {template.ID}, f + "LaunchTemplateSpecification.Version": {"$Latest"}}
	n := 0
	for _, t := range in.InstanceTypes {
		for _, subnet := range c.config.SubnetIDs {
			n++
			p.Set(fmt.Sprintf("%sOverrides.%d.InstanceType", f, n), t)
			p.Set(fmt.Sprintf("%sOverrides.%d.SubnetId", f, n), subnet)
		}
	}
	capacity := "on-demand"
	if in.Spot {
		capacity = "spot"
	}
	p.Set("TargetCapacitySpecification.TotalTargetCapacity", "1")
	p.Set("TargetCapacitySpecification.DefaultTargetCapacityType", capacity)
	p.Set("SpotOptions.AllocationStrategy", "price-capacity-optimized")
	p.Set("OnDemandOptions.AllocationStrategy", "lowest-price")
	var fleet struct {
		Instances []struct {
			IDs       []string `xml:"instanceIds>item"`
			Type      string   `xml:"instanceType"`
			Lifecycle string   `xml:"lifecycle"`
		} `xml:"fleetInstanceSet>item"`
		Errors []struct {
			Code string `xml:"errorCode"`
		} `xml:"errorSet>item"`
	}
	if err := c.call(ctx, "CreateFleet", p, &fleet); err != nil {
		return vm.Instance{}, awsError("create the fleet", err)
	}
	for _, launched := range fleet.Instances {
		if len(launched.IDs) > 0 {
			return vm.Instance{ID: launched.IDs[0], Type: launched.Type, Lifecycle: lifecycle(launched.Lifecycle), State: "pending", Tags: tags}, nil
		}
	}
	codes := []string{}
	for _, e := range fleet.Errors {
		if e.Code != "" && !slices.Contains(codes, e.Code) {
			codes = append(codes, e.Code)
		}
	}
	return vm.Instance{}, fmt.Errorf("%w (%s)", vm.ErrNoCapacity, strings.Join(codes, ", "))
}

// lifecycle maps CreateFleet's lifecycle and DescribeInstances'
// instanceLifecycle, which is absent for on-demand instances.
func lifecycle(l string) string {
	if l == "spot" {
		return "spot"
	}
	return "on-demand"
}

type ec2Instance struct {
	ID         string    `xml:"instanceId"`
	Type       string    `xml:"instanceType"`
	Lifecycle  string    `xml:"instanceLifecycle"`
	State      string    `xml:"instanceState>name"`
	Reason     string    `xml:"stateReason>code"`
	LaunchTime time.Time `xml:"launchTime"`
	Tags       []struct {
		Key   string `xml:"key"`
		Value string `xml:"value"`
	} `xml:"tagSet>item"`
}

type describeInstances struct {
	Instances []ec2Instance `xml:"reservationSet>item>instancesSet>item"`
	NextToken string        `xml:"nextToken"`
}

func instance(i ec2Instance) vm.Instance {
	out := vm.Instance{ID: i.ID, Type: i.Type, Lifecycle: lifecycle(i.Lifecycle), State: i.State, StateReason: i.Reason, LaunchedAt: i.LaunchTime, Tags: map[string]string{}}
	for _, t := range i.Tags {
		out.Tags[t.Key] = t.Value
	}
	return out
}

func (c *Client) State(ctx context.Context, id string) (vm.Instance, error) {
	var out describeInstances
	err := c.call(ctx, "DescribeInstances", url.Values{"InstanceId.1": {id}}, &out)
	var api *awsquery.Error
	if errors.As(err, &api) && api.Code == "InvalidInstanceID.NotFound" {
		return vm.Instance{}, vm.ErrNotFound
	}
	if err != nil {
		return vm.Instance{}, awsError("describe the instance", err)
	}
	for _, i := range out.Instances {
		if i.ID == id {
			return instance(i), nil
		}
	}
	return vm.Instance{}, vm.ErrNotFound
}

// List returns instances carrying every given tag and this installation's.
// Terminated instances stay listed for about an hour, with their reason.
func (c *Client) List(ctx context.Context, tags map[string]string) ([]vm.Instance, error) {
	filters := map[string]string{vm.TagInstallation: c.config.Installation}
	for k, v := range tags {
		filters[k] = v
	}
	p := url.Values{"MaxResults": {"1000"}}
	n := 0
	for k, v := range filters {
		n++
		p.Set(fmt.Sprintf("Filter.%d.Name", n), "tag:"+k)
		p.Set(fmt.Sprintf("Filter.%d.Value.1", n), v)
	}
	out := []vm.Instance{}
	for page := 0; page < 10; page++ {
		var result describeInstances
		if err := c.call(ctx, "DescribeInstances", p, &result); err != nil {
			return nil, awsError("list instances", err)
		}
		for _, i := range result.Instances {
			out = append(out, instance(i))
		}
		if result.NextToken == "" {
			return out, nil
		}
		p.Set("NextToken", result.NextToken)
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
	return awsError("terminate the instance", c.call(ctx, "TerminateInstances", url.Values{"InstanceId.1": {id}}, &struct{}{}))
}

// RunsOn publishes its GitHub-compatible runner images from this account and
// replaces them every couple of weeks, so they are looked up, never pinned.
const runsOnOwner = "135269210855"

var runsOnImages struct {
	sync.Mutex
	found map[string]runsOnImage // region/arch
}

type runsOnImage struct {
	id string
	at time.Time
}

// Image resolves runs-on-full to RunsOn's newest Ubuntu 24.04 full image for
// the capacity's architecture, cached for an hour. AMI IDs and resolve:ssm:
// parameters pass through, and hakopod-minimal is resolved by the caller.
func (c *Client) Image(ctx context.Context, setting string) (string, error) {
	if setting != vm.ImageRunsOnFull {
		return setting, nil
	}
	key := c.config.Region + "/" + c.config.Architecture
	runsOnImages.Lock()
	cached := runsOnImages.found[key]
	runsOnImages.Unlock()
	if cached.id != "" && time.Since(cached.at) < time.Hour {
		return cached.id, nil
	}
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[c.config.Architecture]
	p := url.Values{"Owner.1": {runsOnOwner}, "Filter.1.Name": {"name"}, "Filter.1.Value.1": {"runs-on-v2.2-ubuntu24-full-" + arch + "-*"},
		"Filter.2.Name": {"state"}, "Filter.2.Value.1": {"available"}}
	var out describeImages
	if err := c.call(ctx, "DescribeImages", p, &out); err != nil {
		return "", awsError("find RunsOn's runner image", err)
	}
	newest := ec2Image{}
	for _, i := range out.Images {
		if i.Created.After(newest.Created) {
			newest = i
		}
	}
	if newest.ID == "" {
		return "", fmt.Errorf("RunsOn publishes no Ubuntu 24.04 %s runner image in %s", arch, c.config.Region)
	}
	runsOnImages.Lock()
	if runsOnImages.found == nil {
		runsOnImages.found = map[string]runsOnImage{}
	}
	runsOnImages.found[key] = runsOnImage{newest.ID, time.Now()}
	runsOnImages.Unlock()
	return newest.ID, nil
}

type ec2Image struct {
	ID        string    `xml:"imageId"`
	State     string    `xml:"imageState"`
	Created   time.Time `xml:"creationDate"`
	Snapshots []string  `xml:"blockDeviceMapping>item>ebs>snapshotId"`
	Tags      []struct {
		Key   string `xml:"key"`
		Value string `xml:"value"`
	} `xml:"tagSet>item"`
}

type describeImages struct {
	Images []ec2Image `xml:"imagesSet>item"`
}

// own rechecks the installation tag before any change to an instance, as
// Terminate does.
func (c *Client) own(ctx context.Context, id string) (vm.Instance, error) {
	current, err := c.State(ctx, id)
	if err != nil {
		return current, err
	}
	if current.Tags[vm.TagInstallation] != c.config.Installation {
		return current, fmt.Errorf("instance %s is not tagged for this installation", id)
	}
	return current, nil
}

// Claim gives a stopped standby this job: its new tags, the job's user data,
// termination on shutdown, then a start. A standby that is not stopped is
// refused; the caller terminates it and falls back to a cold launch.
func (c *Client) Claim(ctx context.Context, id, userData string, tags map[string]string) error {
	current, err := c.own(ctx, id)
	if err != nil {
		return err
	}
	if current.State != "stopped" {
		return fmt.Errorf("standby %s is %s, not stopped", id, current.State)
	}
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	p := url.Values{"ResourceId.1": {id}}
	for i, k := range keys {
		p.Set(fmt.Sprintf("Tag.%d.Key", i+1), k)
		p.Set(fmt.Sprintf("Tag.%d.Value", i+1), tags[k])
	}
	if err = c.call(ctx, "CreateTags", p, &struct{}{}); err != nil {
		return awsError("tag the standby", err)
	}
	p = url.Values{"InstanceId": {id}, "InstanceInitiatedShutdownBehavior.Value": {"terminate"}}
	if err = c.call(ctx, "ModifyInstanceAttribute", p, &struct{}{}); err != nil {
		return awsError("set the standby to terminate on shutdown", err)
	}
	p = url.Values{"InstanceId": {id}, "UserData.Value": {base64.StdEncoding.EncodeToString([]byte(userData))}}
	if err = c.call(ctx, "ModifyInstanceAttribute", p, &struct{}{}); err != nil {
		return awsError("give the standby its job", err)
	}
	return awsError("start the standby", c.call(ctx, "StartInstances", url.Values{"InstanceId.1": {id}}, &struct{}{}))
}

func (c *Client) ConsoleOutput(ctx context.Context, id string) (string, error) {
	var out struct {
		Output string `xml:"output"`
	}
	if err := c.call(ctx, "GetConsoleOutput", url.Values{"InstanceId": {id}, "Latest": {"true"}}, &out); err != nil {
		return "", awsError("read the console output", err)
	}
	text, err := base64.StdEncoding.DecodeString(out.Output)
	return string(text), err
}

// CreateImage images a stopped instance of this installation. The image and
// its snapshots are tagged on creation, which the documented policy requires.
func (c *Client) CreateImage(ctx context.Context, instance, name string, tags map[string]string) (string, error) {
	if _, err := c.own(ctx, instance); err != nil {
		return "", err
	}
	all := map[string]string{vm.TagInstallation: c.config.Installation}
	for k, v := range tags {
		all[k] = v
	}
	p := url.Values{"InstanceId": {instance}, "Name": {name}, "Description": {"Hakopod Actions runner image"}}
	tagSpec(p, "TagSpecification.1", "image", all)
	tagSpec(p, "TagSpecification.2", "snapshot", all)
	var out struct {
		ID string `xml:"imageId"`
	}
	if err := c.call(ctx, "CreateImage", p, &out); err != nil {
		return "", awsError("create the image", err)
	}
	return out.ID, nil
}

func (c *Client) describeImages(ctx context.Context, p url.Values) ([]ec2Image, error) {
	p.Set("Owner.1", "self")
	var out describeImages
	if err := c.call(ctx, "DescribeImages", p, &out); err != nil {
		return nil, awsError("list images", err)
	}
	return out.Images, nil
}

func (c *Client) Images(ctx context.Context, tags map[string]string) ([]vm.Image, error) {
	filters := map[string]string{vm.TagInstallation: c.config.Installation}
	for k, v := range tags {
		filters[k] = v
	}
	p := url.Values{}
	n := 0
	for k, v := range filters {
		n++
		p.Set(fmt.Sprintf("Filter.%d.Name", n), "tag:"+k)
		p.Set(fmt.Sprintf("Filter.%d.Value.1", n), v)
	}
	found, err := c.describeImages(ctx, p)
	if err != nil {
		return nil, err
	}
	out := []vm.Image{}
	for _, i := range found {
		image := vm.Image{ID: i.ID, State: i.State, CreatedAt: i.Created, Tags: map[string]string{}}
		for _, t := range i.Tags {
			image.Tags[t.Key] = t.Value
		}
		out = append(out, image)
	}
	return out, nil
}

// DeleteImage deregisters an image tagged for this installation, then deletes
// its snapshots, which deregistering leaves behind.
func (c *Client) DeleteImage(ctx context.Context, id string) error {
	var found describeImages
	err := c.call(ctx, "DescribeImages", url.Values{"Owner.1": {"self"}, "ImageId.1": {id}}, &found)
	var api *awsquery.Error
	if errors.As(err, &api) && api.Code == "InvalidAMIID.NotFound" || err == nil && len(found.Images) == 0 {
		return nil
	}
	if err != nil {
		return awsError("read the image", err)
	}
	image := found.Images[0]
	ours := false
	for _, t := range image.Tags {
		ours = ours || t.Key == vm.TagInstallation && t.Value == c.config.Installation
	}
	if !ours {
		return fmt.Errorf("image %s is not tagged for this installation; not deleting it", id)
	}
	if err = c.call(ctx, "DeregisterImage", url.Values{"ImageId": {id}}, &struct{}{}); err != nil {
		return awsError("deregister the image", err)
	}
	for _, snapshot := range image.Snapshots {
		if err = c.call(ctx, "DeleteSnapshot", url.Values{"SnapshotId": {snapshot}}, &struct{}{}); err != nil {
			return awsError("delete the image's snapshot", err)
		}
	}
	return nil
}

// Check assumes the role and reads the configured subnets and security
// groups. Inbound rules are reported: a runner needs none.
func (c *Client) Check(ctx context.Context) ([]string, error) {
	p := url.Values{}
	list(p, "SubnetId", c.config.SubnetIDs)
	if err := c.call(ctx, "DescribeSubnets", p, &struct{}{}); err != nil {
		return nil, awsError("read the subnets", err)
	}
	p = url.Values{}
	list(p, "GroupId", c.config.SecurityGroupIDs)
	var groups struct {
		Groups []struct {
			ID      string     `xml:"groupId"`
			Inbound []struct{} `xml:"ipPermissions>item"`
		} `xml:"securityGroupInfo>item"`
	}
	if err := c.call(ctx, "DescribeSecurityGroups", p, &groups); err != nil {
		return nil, awsError("read the security groups", err)
	}
	warnings := []string{}
	for _, g := range groups.Groups {
		if len(g.Inbound) > 0 {
			warnings = append(warnings, fmt.Sprintf("security group %s allows inbound traffic; runners need none", g.ID))
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
	var api *awsquery.Error
	if errors.As(err, &api) {
		return fmt.Errorf("AWS could not %s: %s", action, api.Error())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("AWS could not %s; check that Hakopod can assume the capacity's role", action)
}

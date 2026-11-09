package aws

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/hakopod/hakopod/internal/actions/vm"
	"github.com/hakopod/hakopod/internal/awsquery"
)

// fakeEC2 answers the EC2 Query API with XML shaped like AWS's documented
// examples and records each request's form.
type fakeEC2 struct {
	mu        sync.Mutex
	calls     map[string][]url.Values
	fleet     string
	instances map[string]string // id -> <item> of instancesSet
}

func (f *fakeEC2) form(action string) url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls[action]) == 0 {
		return nil
	}
	return f.calls[action][len(f.calls[action])-1]
}

func (f *fakeEC2) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	action := r.Form.Get("Action")
	f.mu.Lock()
	f.calls[action] = append(f.calls[action], r.Form)
	f.mu.Unlock()
	if !strings.Contains(r.Header.Get("Authorization"), "/eu-central-1/ec2/aws4_request") || r.Form.Get("Version") != "2016-11-15" {
		http.Error(w, "unsigned", 403)
		return
	}
	switch action {
	case "CreateLaunchTemplate":
		w.Write([]byte(`<CreateLaunchTemplateResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/">
  <requestId>39f5ee2d-b41a-4bd2-8b3b-1b9e2e2f6c1c</requestId>
  <launchTemplate>
    <createTime>2017-10-31T11:38:52.000Z</createTime>
    <createdBy>arn:aws:iam::123456789012:root</createdBy>
    <defaultVersionNumber>1</defaultVersionNumber>
    <latestVersionNumber>1</latestVersionNumber>
    <launchTemplateId>lt-1</launchTemplateId>
    <launchTemplateName>MyLaunchTemplate</launchTemplateName>
  </launchTemplate>
</CreateLaunchTemplateResponse>`))
	case "CreateFleet":
		w.Write([]byte(f.fleet))
	case "DescribeInstances":
		body := `<DescribeInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>8f7724cf</requestId><reservationSet>`
		ids := []string{}
		for i := 1; r.Form.Get("InstanceId."+strconv.Itoa(i)) != ""; i++ {
			ids = append(ids, r.Form.Get("InstanceId."+strconv.Itoa(i)))
		}
		if len(ids) == 0 {
			for id := range f.instances {
				ids = append(ids, id)
			}
		}
		for _, id := range ids {
			item, ok := f.instances[id]
			if !ok {
				w.WriteHeader(400)
				w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<Response><Errors><Error><Code>InvalidInstanceID.NotFound</Code><Message>The instance ID '` + id + `' does not exist</Message></Error></Errors><RequestID>ea966190-f9aa-478e-9ede-example</RequestID></Response>`))
				return
			}
			// One reservation per instance, as fleets launch them.
			body += `<item><reservationId>r-1</reservationId><ownerId>123456789012</ownerId><groupSet/><instancesSet>` + item + `</instancesSet></item>`
		}
		w.Write([]byte(body + `</reservationSet></DescribeInstancesResponse>`))
	case "DescribeImages":
		body := `<DescribeImagesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>1</requestId><imagesSet>`
		if r.Form.Get("ImageId.1") != "ami-other" {
			body += image("ami-old", "2026-10-01T00:00:00.000Z", "inst") + image("ami-new", "2026-11-01T00:00:00.000Z", "inst")
		} else {
			body += image("ami-other", "2026-11-01T00:00:00.000Z", "someone-else")
		}
		w.Write([]byte(body + `</imagesSet></DescribeImagesResponse>`))
	case "CreateImage":
		w.Write([]byte(`<CreateImageResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>1</requestId><imageId>ami-built</imageId></CreateImageResponse>`))
	case "TerminateInstances", "DeleteLaunchTemplate", "DescribeSubnets", "CreateTags", "ModifyInstanceAttribute", "StartInstances", "DeregisterImage", "DeleteSnapshot":
		w.Write([]byte(`<` + action + `Response><requestId>1</requestId></` + action + `Response>`))
	case "DescribeSecurityGroups":
		w.Write([]byte(`<DescribeSecurityGroupsResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/">
   <requestId>59dbff89-35bd-4eac-99ed-be587EXAMPLE</requestId>
   <securityGroupInfo>
      <item><ownerId>123456789012</ownerId><groupId>sg-1</groupId><groupName>WebServers</groupName>
         <ipPermissions><item><ipProtocol>tcp</ipProtocol><fromPort>80</fromPort><toPort>80</toPort><groups/>
            <ipRanges><item><cidrIp>0.0.0.0/0</cidrIp></item></ipRanges></item></ipPermissions>
         <ipPermissionsEgress><item><ipProtocol>-1</ipProtocol><ipRanges><item><cidrIp>0.0.0.0/0</cidrIp></item></ipRanges></item></ipPermissionsEgress>
      </item>
      <item><ownerId>123456789012</ownerId><groupId>sg-2</groupId><groupName>Runners</groupName><ipPermissions/>
         <ipPermissionsEgress><item><ipProtocol>-1</ipProtocol><ipRanges><item><cidrIp>0.0.0.0/0</cidrIp></item></ipRanges></item></ipPermissionsEgress>
      </item>
   </securityGroupInfo>
</DescribeSecurityGroupsResponse>`))
	default:
		http.Error(w, "unknown action", 400)
	}
}

func tagged(id, installation, state, reason string) string {
	lifecycle := `<instanceLifecycle>spot</instanceLifecycle>`
	if id == "i-running" {
		lifecycle = "" // on-demand instances have none
	}
	return `<item><instanceId>` + id + `</instanceId><imageId>ami-1</imageId>
<instanceState><code>16</code><name>` + state + `</name></instanceState>
<stateReason><code>` + reason + `</code><message>` + reason + `</message></stateReason>
<instanceType>c7i.xlarge</instanceType><launchTime>2026-11-09T10:00:00.000Z</launchTime>
<blockDeviceMapping><item><deviceName>/dev/sda1</deviceName><ebs><volumeId>vol-1</volumeId></ebs></item></blockDeviceMapping>` + lifecycle + `
<tagSet><item><key>` + vm.TagInstallation + `</key><value>` + installation + `</value></item><item><key>Name</key><value>runner</value></item></tagSet>
</item>`
}

func image(id, created, installation string) string {
	return `<item><imageId>` + id + `</imageId><imageState>available</imageState><creationDate>` + created + `</creationDate>
<blockDeviceMapping><item><deviceName>/dev/sda1</deviceName><ebs><snapshotId>snap-` + id + `</snapshotId></ebs></item></blockDeviceMapping>
<tagSet><item><key>` + vm.TagInstallation + `</key><value>` + installation + `</value></item></tagSet></item>`
}

func testClient(t *testing.T, f *fakeEC2) *Client {
	if f.calls == nil {
		f.calls = map[string][]url.Values{}
	}
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	creds := sdk.CredentialsProviderFunc(func(context.Context) (sdk.Credentials, error) {
		return sdk.Credentials{AccessKeyID: "AKIDTEST", SecretAccessKey: "secret"}, nil
	})
	return &Client{
		ec2:    awsquery.Client{HTTP: server.Client(), Credentials: creds, Region: "eu-central-1", Endpoint: func(string) string { return server.URL + "/" }},
		config: Config{Installation: "inst", Region: "eu-central-1", SubnetIDs: []string{"subnet-a", "subnet-b"}, SecurityGroupIDs: []string{"sg-1"}, Architecture: "arm64", DiskGiB: 100},
	}
}

// From the CreateFleet API reference, instant fleet example.
const fleetLaunched = `<CreateFleetResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/">
    <requestId>b2b0a8d4-ee2f-4ad6-bd7d-EXAMPLE</requestId>
    <fleetId>fleet-12a34b55-67cd-8ef9-ba9b-9208dEXAMPLE</fleetId>
    <errorSet/>
    <fleetInstanceSet>
        <item>
            <instanceIds><item>i-1</item></instanceIds>
            <instanceType>c7g.xlarge</instanceType>
            <launchTemplateAndOverrides>
                <launchTemplateSpecification><launchTemplateId>lt-1</launchTemplateId><version>1</version></launchTemplateSpecification>
                <overrides><instanceType>c7g.xlarge</instanceType><subnetId>subnet-a</subnetId></overrides>
            </launchTemplateAndOverrides>
            <lifecycle>spot</lifecycle>
        </item>
    </fleetInstanceSet>
</CreateFleetResponse>`

const fleetFailed = `<CreateFleetResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/">
    <requestId>b2b0a8d4</requestId>
    <fleetId>fleet-1</fleetId>
    <errorSet>
        <item>
            <errorCode>InsufficientInstanceCapacity</errorCode>
            <errorMessage>We currently do not have sufficient c7g.xlarge capacity.</errorMessage>
            <launchTemplateAndOverrides><overrides><instanceType>c7g.xlarge</instanceType></overrides></launchTemplateAndOverrides>
            <lifecycle>on-demand</lifecycle>
        </item>
        <item><errorCode>InsufficientInstanceCapacity</errorCode><lifecycle>on-demand</lifecycle></item>
    </errorSet>
    <fleetInstanceSet/>
</CreateFleetResponse>`

func TestLaunch(t *testing.T) {
	f := &fakeEC2{fleet: fleetLaunched}
	got, err := testClient(t, f).Launch(context.Background(), vm.LaunchInput{Name: "slot1", Tags: map[string]string{vm.TagSlot: "slot1"}, InstanceTypes: []string{"c7g.xlarge", "m7g.xlarge"}, Spot: true, UserData: "#!/bin/bash\n"})
	if err != nil || got.ID != "i-1" || got.Lifecycle != "spot" || got.Type != "c7g.xlarge" {
		t.Fatal(got, err)
	}
	lt := f.form("CreateLaunchTemplate")
	want := map[string]string{
		"LaunchTemplateData.ImageId":                                      "resolve:ssm:/aws/service/canonical/ubuntu/server/24.04/stable/current/arm64/hvm/ebs-gp3/ami-id",
		"LaunchTemplateData.InstanceInitiatedShutdownBehavior":            "terminate",
		"LaunchTemplateData.MetadataOptions.HttpTokens":                   "required",
		"LaunchTemplateData.MetadataOptions.HttpPutResponseHopLimit":      "1",
		"LaunchTemplateData.SecurityGroupId.1":                            "sg-1",
		"LaunchTemplateData.BlockDeviceMapping.1.DeviceName":              "/dev/sda1",
		"LaunchTemplateData.BlockDeviceMapping.1.Ebs.VolumeSize":          "100",
		"LaunchTemplateData.BlockDeviceMapping.1.Ebs.Encrypted":           "true",
		"LaunchTemplateData.BlockDeviceMapping.1.Ebs.DeleteOnTermination": "true",
		"LaunchTemplateData.TagSpecification.1.ResourceType":              "instance",
		"LaunchTemplateData.TagSpecification.1.Tag.1.Key":                 vm.TagInstallation,
		"LaunchTemplateData.TagSpecification.1.Tag.1.Value":               "inst",
		"LaunchTemplateData.TagSpecification.1.Tag.2.Key":                 vm.TagSlot,
		"LaunchTemplateData.TagSpecification.1.Tag.2.Value":               "slot1",
		"LaunchTemplateData.TagSpecification.2.ResourceType":              "volume",
		"TagSpecification.1.ResourceType":                                 "launch-template",
	}
	for k, v := range want {
		if lt.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, lt.Get(k), v)
		}
	}
	if decoded, _ := base64.StdEncoding.DecodeString(lt.Get("LaunchTemplateData.UserData")); string(decoded) != "#!/bin/bash\n" {
		t.Fatal("user data", string(decoded))
	}
	if !strings.HasPrefix(lt.Get("LaunchTemplateName"), "hakopod-slot1-") {
		t.Fatal(lt.Get("LaunchTemplateName"))
	}
	fleet := f.form("CreateFleet")
	if fleet.Get("Type") != "instant" || fleet.Get("TargetCapacitySpecification.DefaultTargetCapacityType") != "spot" ||
		fleet.Get("SpotOptions.AllocationStrategy") != "price-capacity-optimized" || fleet.Get("LaunchTemplateConfigs.1.LaunchTemplateSpecification.LaunchTemplateId") != "lt-1" ||
		fleet.Get("LaunchTemplateConfigs.1.Overrides.4.InstanceType") != "m7g.xlarge" || fleet.Get("LaunchTemplateConfigs.1.Overrides.4.SubnetId") != "subnet-b" ||
		fleet.Get("LaunchTemplateConfigs.1.Overrides.5.InstanceType") != "" {
		t.Fatal("fleet request", fleet)
	}
	if d := f.form("DeleteLaunchTemplate"); d.Get("LaunchTemplateId") != "lt-1" {
		t.Fatal("the launch template with the runner configuration was kept")
	}

	f = &fakeEC2{fleet: fleetFailed}
	_, err = testClient(t, f).Launch(context.Background(), vm.LaunchInput{Name: "slot2", InstanceTypes: []string{"c7g.xlarge"}})
	if !errors.Is(err, vm.ErrNoCapacity) || !strings.HasSuffix(err.Error(), "(InsufficientInstanceCapacity)") ||
		f.form("CreateFleet").Get("TargetCapacitySpecification.DefaultTargetCapacityType") != "on-demand" || f.form("DeleteLaunchTemplate") == nil {
		t.Fatal("failed on-demand launch", err)
	}
}

func TestStateTerminateAndCheck(t *testing.T) {
	f := &fakeEC2{instances: map[string]string{
		"i-ours":    tagged("i-ours", "inst", "terminated", "Server.SpotInstanceTermination"),
		"i-running": tagged("i-running", "inst", "running", ""),
		"i-other":   tagged("i-other", "someone-else", "running", ""),
	}}
	c := testClient(t, f)
	ctx := context.Background()
	got, err := c.State(ctx, "i-ours")
	if err != nil || !got.Interrupted() || !got.Gone() || got.Lifecycle != "spot" || got.Tags["Name"] != "runner" || !got.LaunchedAt.Equal(time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)) {
		t.Fatal(got, err)
	}
	if got, _ := c.State(ctx, "i-running"); got.Lifecycle != "on-demand" || got.State != "running" {
		t.Fatal(got)
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
	if err = c.Terminate(ctx, "i-running"); err != nil || len(f.calls["TerminateInstances"]) != 1 || f.form("TerminateInstances").Get("InstanceId.1") != "i-running" {
		t.Fatal(f.calls["TerminateInstances"], err)
	}
	listed, err := c.List(ctx, map[string]string{vm.TagSlot: "slot1"})
	if err != nil || len(listed) != 3 {
		t.Fatal(listed, err)
	}
	filters := map[string]string{}
	form := f.form("DescribeInstances")
	for _, n := range []string{"1", "2"} {
		filters[form.Get("Filter."+n+".Name")] = form.Get("Filter." + n + ".Value.1")
	}
	if filters["tag:"+vm.TagInstallation] != "inst" || filters["tag:"+vm.TagSlot] != "slot1" || form.Get("MaxResults") != "1000" {
		t.Fatal("list filters", form)
	}
	warnings, err := c.Check(ctx)
	if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0], "sg-1") || f.form("DescribeSubnets").Get("SubnetId.2") != "subnet-b" || f.form("DescribeSecurityGroups").Get("GroupId.1") != "sg-1" {
		t.Fatal(warnings, err)
	}
}

func TestAWSErrorKeepsTheCode(t *testing.T) {
	err := awsError("create the fleet", &awsquery.Error{Status: 400, Code: "UnauthorizedOperation", Message: "encoded authorization failure message"})
	if err.Error() != "AWS could not create the fleet: UnauthorizedOperation" {
		t.Fatal(err)
	}
	if err := awsError("read the subnets", errors.New("dial tcp: no route")); !strings.Contains(err.Error(), "assume the capacity's role") {
		t.Fatal(err)
	}
}

func TestImagesAndClaim(t *testing.T) {
	f := &fakeEC2{fleet: fleetLaunched, instances: map[string]string{
		"i-standby": tagged("i-standby", "inst", "stopped", ""),
		"i-running": tagged("i-running", "inst", "running", ""),
		"i-other":   tagged("i-other", "someone-else", "stopped", ""),
	}}
	c := testClient(t, f)
	ctx := context.Background()
	got, err := c.Image(ctx, vm.ImageRunsOnFull)
	form := f.form("DescribeImages")
	if err != nil || got != "ami-new" || form.Get("Owner.1") != "135269210855" || form.Get("Filter.1.Value.1") != "runs-on-v2.2-ubuntu24-full-arm64-*" {
		t.Fatal(got, err, form)
	}
	if again, _ := c.Image(ctx, vm.ImageRunsOnFull); again != "ami-new" || len(f.calls["DescribeImages"]) != 1 {
		t.Fatal("RunsOn's image was not cached")
	}
	if got, _ := c.Image(ctx, "resolve:ssm:/team/runner-ami"); got != "resolve:ssm:/team/runner-ami" {
		t.Fatal(got)
	}

	if _, err = c.Launch(ctx, vm.LaunchInput{Name: "standby", InstanceTypes: []string{"t4g.small"}, Image: "ami-new", StopOnShutdown: true, DiskGiB: 8, VolumeInitRate: 300}); err != nil {
		t.Fatal(err)
	}
	lt := f.form("CreateLaunchTemplate")
	if lt.Get("LaunchTemplateData.ImageId") != "ami-new" || lt.Get("LaunchTemplateData.InstanceInitiatedShutdownBehavior") != "stop" || lt.Get("LaunchTemplateData.BlockDeviceMapping.1.Ebs.VolumeSize") != "8" ||
		lt.Get("LaunchTemplateData.BlockDeviceMapping.1.Ebs.VolumeInitializationRate") != "300" {
		t.Fatal("standby launch", lt)
	}

	if err = c.Claim(ctx, "i-other", "#!/bin/bash\n", nil); err == nil {
		t.Fatal("claimed another installation's instance")
	}
	if err = c.Claim(ctx, "i-running", "#!/bin/bash\n", nil); err == nil {
		t.Fatal("claimed a standby that is not stopped")
	}
	if err = c.Claim(ctx, "i-standby", "#!/bin/bash\n", map[string]string{vm.TagSlot: "slot1", vm.TagPoolState: "claimed"}); err != nil {
		t.Fatal(err)
	}
	tags, modify := f.form("CreateTags"), f.calls["ModifyInstanceAttribute"]
	if tags.Get("ResourceId.1") != "i-standby" || tags.Get("Tag.1.Key") != vm.TagPoolState || tags.Get("Tag.2.Value") != "slot1" || len(modify) != 2 ||
		modify[0].Get("InstanceInitiatedShutdownBehavior.Value") != "terminate" || modify[1].Get("UserData.Value") != base64.StdEncoding.EncodeToString([]byte("#!/bin/bash\n")) ||
		f.form("StartInstances").Get("InstanceId.1") != "i-standby" {
		t.Fatal("claim requests", tags, modify)
	}

	id, err := c.CreateImage(ctx, "i-standby", "hakopod-minimal-ci", map[string]string{vm.TagCapacity: "ci"})
	create := f.form("CreateImage")
	if err != nil || id != "ami-built" || create.Get("TagSpecification.1.ResourceType") != "image" || create.Get("TagSpecification.2.ResourceType") != "snapshot" ||
		create.Get("TagSpecification.2.Tag.2.Key") != vm.TagInstallation || create.Get("TagSpecification.2.Tag.2.Value") != "inst" {
		t.Fatal(id, err, create)
	}
	if _, err = c.CreateImage(ctx, "i-other", "x", nil); err == nil {
		t.Fatal("imaged another installation's instance")
	}
	if err = c.DeleteImage(ctx, "ami-other"); err == nil || f.calls["DeregisterImage"] != nil {
		t.Fatal("deleted another installation's image")
	}
	if err = c.DeleteImage(ctx, "ami-old"); err != nil || f.form("DeregisterImage").Get("ImageId") != "ami-old" || f.form("DeleteSnapshot").Get("SnapshotId") != "snap-ami-old" {
		t.Fatal("image not deleted with its snapshot", err)
	}
}

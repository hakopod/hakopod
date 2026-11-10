"""Actions capacity: cloud accounts where GitHub pools run each job on a single-use VM."""
schemas["Actions"]["properties"]["capacity"] = {"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$", "description": "GitHub pools only. Runs each job on a single-use VM in this installation Actions capacity instead of a runner pod. The capacity decides the machine, so size, resources, architecture, node_name, workspace_size_gib and cache are refused. The pool's project and environment must be allowed to use it."}
schemas["Actions"]["properties"]["spot_retries"] = {"type": "integer", "minimum": 0, "maximum": 5, "description": "Requires capacity. Reruns of a workflow run's failed jobs after spot interruptions, per run. Defaults to 2; 0 turns automatic reruns off. Reruns repeat every failed job of the run."}
schemas["ActionsSlot"]["properties"].update({
    "instance_id": {**S, "description": "VM slots: the cloud instance running this slot's job."},
    "instance_type": S,
    "instance_lifecycle": {"type": "string", "enum": ["spot", "on-demand"]},
    "instance_source": {"type": "string", "enum": ["cold", "standby"], "description": "VM slots: launched for this job, or a stopped standby of the capacity's warm pool started for it."},
})
schemas["ActionsEviction"] = obj({
    "slot_id": S, "capacity": S, "instance_id": S, "instance_type": S, "evicted_at": T,
    "repository": S, "run_id": I, "run_attempt": I,
    "rerun_at": {**T, "description": "When Hakopod decided about a rerun of this run attempt."},
    "rerun_error": {**S, "description": "Why the run was not rerun, for example a jobs credential without Actions write permission."},
}, ["slot_id", "capacity", "instance_id", "instance_type", "evicted_at"])
schemas["ActionsEviction"]["description"] = "A spot interruption of one of the pool's VMs, kept for 30 days."
pool_items = paths["/applications/{id}/actions"]["get"]["responses"]["200"]["content"]["application/json"]["schema"]["properties"]["items"]["items"]
pool_items["properties"]["evictions"] = array(ref("ActionsEviction"))
pool_items["required"].append("evictions")

ID_LIST = lambda pattern, most: {"type": "array", "items": {"type": "string", "pattern": pattern}, "minItems": 1, "maxItems": most, "uniqueItems": True}
schemas["ActionsCapacity"] = obj({
    "name": {"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$"},
    "cloud": {"type": "string", "enum": ["aws", "gcp"], "description": "aws runs jobs on EC2; gcp on Compute Engine."},
    "region": {**S, "description": "AWS region, for example eu-central-1, or Google Cloud region, for example us-central1."},
    "role_arn": {**S, "description": "AWS only, required there. IAM role Hakopod assumes from its own AWS credential chain."},
    "external_id": {**S, "description": "AWS only. External ID for the role's trust policy. Omit to use the installation's generated one."},
    "project": {"type": "string", "pattern": "^[a-z][a-z0-9-]{4,28}[a-z0-9]$", "description": "Google Cloud only, required there. The project runners are launched in."},
    "zones": {**ID_LIST("^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$", 10), "description": "Google Cloud only, required there. Zones of the region to try, in order."},
    "service_account": {**S, "description": "Google Cloud only, required there. Service account Hakopod impersonates from its own Google identity; grant that identity Service Account Token Creator on it. Runner VMs get no service account."},
    "subnet_ids": {**ID_LIST("^(subnet-[0-9a-f]{8,17}|[a-z]([-a-z0-9]{0,61}[a-z0-9])?)$", 10), "description": "AWS: subnets with outbound access to GitHub. Google Cloud: one subnetwork name in the region. Nothing inbound is needed."},
    "security_group_ids": {**ID_LIST("^sg-[0-9a-f]{8,17}$", 5), "description": "AWS only, required there. Security groups for the runners. They need no inbound rules."},
    "instance_types": {**ID_LIST("^([a-z][a-z0-9-]{0,15}\\.[a-z0-9]{1,16}|[a-z][a-z0-9]{1,5}-[a-z0-9-]{1,40})$", 20), "description": "EC2 instance types or Compute Engine machine types, tried in order."},
    "architecture": {"type": "string", "enum": ["amd64", "arm64"]},
    "spot": {**B, "description": "Launch on spot capacity; false launches on-demand only."},
    "disk_gib": {"type": "integer", "minimum": 20, "maximum": 2000, "description": "Root volume size. Defaults to 100."},
    "max_instances": {"type": "integer", "minimum": 1, "maximum": 1000, "description": "Hard ceiling on instances across every pool using this capacity."},
    "environments": {"type": "array", "items": S, "minItems": 1, "maxItems": 100, "description": "project/environment pairs allowed to use this capacity, or \"*\" for every environment."},
    "image": {"type": "string", "pattern": "^(runs-on-full|hakopod-minimal|ami-[0-9a-f]{8,17}|resolve:ssm:/[A-Za-z0-9_./-]{1,200}|projects/[a-z][a-z0-9-]{4,28}[a-z0-9]/global/images/(family/)?[a-z]([-a-z0-9]{0,61}[a-z0-9])?)$", "description": "runs-on-full (AWS default): RunsOn's public GitHub-compatible Ubuntu 24.04 image, newest at launch; needs disk_gib of at least 30. hakopod-minimal (Google Cloud default): an image Hakopod builds in this account with build-image. Or your own Ubuntu image: an AMI ID or resolve:ssm: parameter on AWS, a projects/PROJECT/global/images/[family/]NAME path on Google Cloud."},
    "warm_pool": {"type": "integer", "minimum": 0, "maximum": 50, "description": "Stopped on-demand standbys kept ready to start for a job; they count toward max_instances. Defaults to 0."},
}, ["name", "cloud", "region", "subnet_ids", "instance_types", "architecture", "spot", "max_instances", "environments"])
schemas["ActionsCapacityImage"] = obj({
    "capacity": S, "state": {"type": "string", "enum": ["building", "available", "failed"]},
    "builder_id": {**S, "description": "The instance installing the image while it builds."},
    "building_image_id": {**S, "description": "The image being created; launches keep image_id until it is available."},
    "image_id": {**S, "description": "The image launches use."},
    "message": S, "requested_at": T, "updated_at": T,
}, ["capacity", "state", "requested_at", "updated_at"])
schemas["ActionsCapacityImage"]["description"] = "A capacity's hakopod-minimal image build."
schemas["ActionsCapacitySettings"] = obj({"external_id": {**S, "description": "Generated once per installation. Put it in each role's trust policy as sts:ExternalId."}, "installation_id": {**S, "description": "Value of the hakopod-installation tag on every instance; condition the role's TerminateInstances on it."}, "capacities": array(ref("ActionsCapacity")), "revision": I, "images": {**mapping(ref("ActionsCapacityImage")), "description": "hakopod-minimal image builds, keyed by capacity."}}, ["external_id", "installation_id", "capacities", "revision", "images"])
schemas["ActionsCapacityInput"] = obj({"capacities": {"type": "array", "items": ref("ActionsCapacity"), "maxItems": 20}, "expected_revision": I}, ["capacities", "expected_revision"])
schemas["ActionsCapacityTestInput"] = obj({"launch": {**B, "description": "Also launch the smallest on-demand instance of the capacity's architecture and terminate it."}})
schemas["ActionsCapacityTest"] = obj({"capacity": S, "external_id": S, "warnings": array(S), "instance_id": S, "instance_type": S}, ["capacity", "external_id", "warnings"])
route("/settings/actions-capacity", "get", "getActionsCapacity", ref("ActionsCapacitySettings"))
route("/settings/actions-capacity", "put", "putActionsCapacity", ref("ActionsCapacitySettings"), ref("ActionsCapacityInput"))
route("/settings/actions-capacity/{name}/test", "post", "testActionsCapacity", ref("ActionsCapacityTest"), ref("ActionsCapacityTestInput"))
route("/settings/actions-capacity/{name}/build-image", "post", "buildActionsCapacityImage", ref("ActionsCapacityImage"), status="202")
for path, method, text, description in [
    ("/settings/actions-capacity", "get", "Get the Actions capacity", "Installation administrators only. The first read generates the installation's external ID."),
    ("/settings/actions-capacity", "put", "Replace the Actions capacity", "Installation administrators only. Optimistic revision; a capacity still used by a pool cannot be removed. Every change is audited."),
    ("/settings/actions-capacity/{name}/build-image", "post", "Build a hakopod-minimal image", "Installation administrators only. The capacity's image must be hakopod-minimal. The Actions worker launches a stock Ubuntu builder in the capacity's account, installs Docker and the pinned runner, images it and removes older images; launches use the previous image until the new one is available. Audited."),
    ("/settings/actions-capacity/{name}/test", "post", "Test an Actions capacity", "Installation administrators only. Assumes the role and reads the subnets and security groups on AWS; impersonates the service account, reads the subnetwork and reports firewall rules open to the internet on Google Cloud. With launch, starts and terminates one small on-demand instance."),
]:
    paths[path][method]["summary"] = text
    paths[path][method]["description"] = description

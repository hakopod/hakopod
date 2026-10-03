#!/usr/bin/env python3
"""Observe an actual failed owned branch, then delete its parent through Hakopod."""
import base64
import hashlib
import http.client
import importlib.util
import json
import os
from pathlib import Path
import re
import secrets
import socket
import ssl
import time
import urllib.parse

ID = re.compile(r"[0-9a-f]{32}")
DIGEST = re.compile(r"[0-9a-f]{64}")
LIMIT = 1 << 20
PREPARATION_FIELDS = {"run_id", "platform_id", "namespace_uid", "target_platform_id", "target_namespace_uid", "source_revision",
    "tenant_id", "timeline_id", "ancestor_timeline_id", "normalized_request_sha256", "controller_request_hash", "ownership_token_sha256",
    "create_http_status", "create_error_sha256", "ledger_before", "data_prefix_objects_before", "reservation_before_sha256",
    "source_persistent_volume_ids", "target_before", "prior_lifecycle_sha256", "backup_recovery_sha256"}
AFTER_FIELDS = {"preparation_sha256", "data_prefix_objects_after", "delete_operation", "tombstone", "tenant_fence",
                "foreign_authority", "target_after", "source_namespace_absent", "source_persistent_volumes_absent"}


def require(ok, message):
    if not ok:
        raise RuntimeError(message)


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def encoded(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode()


def preserve_raw(path, value):
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "wb") as output:
        output.write(value)
        output.flush()
        os.fsync(output.fileno())


def missing_ancestor_error(status, raw):
    return status == 500 and b"cannot branch off the timeline" in raw.lower() and b"not present in pageserver" in raw.lower()


def branch_request(tenant, timeline, ancestor):
    require(all(ID.fullmatch(v) for v in (tenant, timeline, ancestor)) and timeline != ancestor,
            "partial branch identity is invalid")
    # These explicit nulls are the serialized Branch defaults in the pinned
    # pageserver API model; false read_only and absent ownership are omitted.
    body = {"new_timeline_id": timeline, "ancestor_timeline_id": ancestor,
            "ancestor_start_lsn": None, "pg_version": None}
    raw = encoded(body)
    return body, digest(raw), digest(b"timeline\0" + (tenant + "/" + timeline).encode() + b"\0" + raw)


class LocalTLS(http.client.HTTPSConnection):
    def connect(self):
        self.sock = self._context.wrap_socket(socket.create_connection(("127.0.0.1", self.port), self.timeout),
                                             server_hostname=self.host)


def secret(driver, platform, name, keys):
    require(re.fullmatch(r"[a-z0-9][a-z0-9.-]{0,252}", name), "native secret name is invalid")
    namespace = "managed-platform-" + platform
    driver.claim_namespace(platform, driver.bound[platform][1])
    value = json.loads(driver.k("-n", namespace, "get", "secret", name, "-o", "json"))
    metadata = value["metadata"]
    require(metadata.get("namespace") == namespace and metadata.get("labels", {}).get("hakopod.io/managed-platform-id") == platform and
            metadata.get("ownerReferences") == [{"apiVersion": "v1", "kind": "Namespace", "name": namespace, "uid": driver.bound[platform][0]}],
            "native secret ownership changed")
    data = {key: base64.b64decode(value["data"][key], validate=True) for key in keys}
    require(all(0 < len(v) <= 65536 for v in data.values()), "native secret data exceeds its bound")
    return data


class Controller:
    def __init__(self, driver, platform):
        self.driver, self.platform = driver, platform
        path = Path(__file__).with_name("proxy-stream.py")
        spec = importlib.util.spec_from_file_location("partial_controller_stream", path)
        self.stream_module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.stream_module)
        namespace, (uid, operation) = "managed-platform-" + platform, driver.bound[platform]
        self.target = self.stream_module.ProxyTarget(driver.a.kubeconfig, platform, uid, operation,
            json.loads(Path(driver.a.images).read_text())["storage-controller"],
            lambda *args: json.loads(driver.k(*args)), component="storage-controller")
        bound = self.target()
        deployment = json.loads(driver.k("-n", namespace, "get", "deployment", bound["workload_name"], "-o", "json"))
        names = [v.get("secret", {}).get("secretName") for v in deployment["spec"]["template"]["spec"]["volumes"] if v["name"] == "controller-auth"]
        require(len(names) == 1, "native controller credential binding is ambiguous")
        credentials = secret(driver, platform, names[0], ("token",))
        self.bearer = credentials["token"].decode()
        require(16 <= len(self.bearer) <= 16384 and all(32 < ord(c) < 127 for c in self.bearer), "native controller bearer is malformed")
        self.context = ssl.create_default_context(cafile=str(driver.platform_ca(platform)))
        self.forward = self.stream_module.STREAM.OwnedPodForward(driver.a.local_port + 2, self.target, 4, lifetime=180)

    def __enter__(self):
        self.forward.__enter__()
        return self

    def __exit__(self, *args):
        return self.forward.__exit__(*args)

    def request(self, method, path, body=None, owner=None, deletion=None, bearer=None):
        require(method in ("GET", "POST", "DELETE") and path.startswith(("/v1/tenant/", "/control/v1/tenant/")) and
                len(path) < 512 and "#" not in path, "native controller request is outside the case")
        self.target()
        headers = {"Authorization": "Bearer " + (self.bearer if bearer is None else bearer), "Accept": "application/json"}
        for name, value in (("Hakopod-Ownership-Token", owner), ("Hakopod-Deletion-Token", deletion)):
            if value is not None:
                require(ID.fullmatch(value), "native ownership header is malformed")
                headers[name] = value
        raw = None if body is None else encoded(body)
        if raw is not None:
            headers["Content-Type"] = "application/json"
        connection = LocalTLS("neon-storage-controller", self.driver.a.local_port + 2, timeout=45, context=self.context)
        try:
            connection.request(method, path, body=raw, headers=headers)
            response = connection.getresponse()
            value = response.read(LIMIT + 1)
            require(len(value) <= LIMIT, "native controller response exceeded its bound")
            return response.status, value
        finally:
            connection.close()


def ledger(driver, platform, tenant, timeline):
    require(all(ID.fullmatch(v) for v in (platform, tenant, timeline)), "native ledger scope is malformed")
    require(driver.claim_namespace(platform, driver.bound[platform][1]), "native ledger namespace is absent")
    namespace, pods = driver.pods(platform)
    selected = [p for p in pods if p["metadata"]["labels"].get("hakopod.io/neon-role") == "controller-database"]
    require(len(selected) == 1, "native controller database is ambiguous")
    pod = selected[0]
    owner = pod["metadata"].get("ownerReferences", [])
    workload = json.loads(driver.k("-n", namespace, "get", "statefulset", "neon-controller-database", "-o", "json"))
    require(len(owner) == 1 and owner[0].get("kind") == "StatefulSet" and owner[0].get("uid") == workload["metadata"]["uid"] and
            workload["metadata"].get("ownerReferences") == [{"apiVersion": "v1", "kind": "Namespace", "name": namespace, "uid": driver.bound[platform][0]}],
            "native controller database ownership changed")
    containers = pod["spec"].get("containers", [])
    require(len(containers) == 1 and containers[0]["name"] == "controller-database" and
            containers[0]["image"] == json.loads(Path(driver.a.images).read_text())["controller-database"] and
            containers[0]["image"] == workload["spec"]["template"]["spec"]["containers"][0]["image"],
            "native controller database image changed")
    query = "SELECT coalesce(json_agg(x),'[]'::json) FROM (SELECT resource_kind,external_key,state::text,CASE WHEN result IS NULL THEN NULL ELSE 'present' END AS result,encode(request_hash,'hex') AS request_hash,encode(sha256(convert_to(replace(ownership_token::text,'-',''),'UTF8')),'hex') AS ownership_token_sha256,(SELECT count(*) FROM timelines WHERE tenant_id='" + tenant + "' AND timeline_id='" + timeline + "') AS timeline_rows FROM hakopod_resource_ownership WHERE external_key='" + tenant + "/" + timeline + "' LIMIT 2) x;"
    argv = ["kubectl", "--kubeconfig", driver.a.kubeconfig, "--context", "k3d-hakopod-dev", "--request-timeout=15s", "-n", namespace,
            "exec", "-i", pod["metadata"]["name"], "-c", "controller-database", "--", "env", "PGOPTIONS=-c default_transaction_read_only=on -c statement_timeout=5000",
            "psql", "-XAtqw", "-U", "storage_controller", "-d", "storage_controller", "-v", "ON_ERROR_STOP=1", "-f", "-"]
    return json.loads(driver.command(argv, 20, data=query.encode()))


class Objects:
    def __init__(self, driver, platform, spec):
        import boto3
        from botocore.config import Config
        self.config = spec["neon"]
        endpoint = urllib.parse.urlsplit(self.config["object_storage_url"])
        require(endpoint.scheme == "https" and endpoint.hostname and endpoint.username is None and endpoint.password is None and
                endpoint.path in ("", "/") and not endpoint.query and not endpoint.fragment, "native object endpoint is not verified HTTPS")
        reference = spec["secrets"]["object-storage"]
        credentials = secret(driver, platform, reference["name"] + "-r" + str(reference["revision"]), ("access-key-id", "secret-access-key"))
        self.client = boto3.client("s3", endpoint_url=self.config["object_storage_url"], region_name=self.config["object_storage_region"],
            aws_access_key_id=credentials["access-key-id"].decode(), aws_secret_access_key=credentials["secret-access-key"].decode(),
            config=Config(connect_timeout=5, read_timeout=15, retries={"total_max_attempts": 1}, s3={"addressing_style": "path"}))
        self.bucket, self.prefix = self.config["object_storage_bucket"], self.config["object_storage_prefix"] + "/pageserver/"

    def empty(self, path):
        result = self.client.list_objects_v2(Bucket=self.bucket, Prefix=self.prefix + path, MaxKeys=1)
        require(result.get("KeyCount") == 0 and not result.get("Contents") and result.get("IsTruncated") is False,
                "partial timeline unexpectedly has remote data")
        return 0

    def marker(self, path, optional=False):
        from botocore.exceptions import ClientError
        try:
            response = self.client.get_object(Bucket=self.bucket, Key=self.prefix + path)
        except ClientError as error:
            if optional and error.response.get("Error", {}).get("Code") in ("NoSuchKey", "404", "NotFound"):
                return None
            raise RuntimeError("native ownership marker was unavailable") from None
        try:
            require(response["ContentLength"] <= 16384, "native ownership marker exceeds its bound")
            raw = response["Body"].read(16385)
            require(len(raw) == response["ContentLength"], "native ownership marker length changed")
        finally:
            response["Body"].close()
        return json.loads(raw), digest(raw)


def target_snapshot(driver, platform):
    current = driver.api("GET", "/api/v1/managed-platforms/" + platform)
    probe = driver.api("POST", "/api/v1/managed-platforms/" + platform + "/native-probe", {"expected_revision": current["revision"]})
    namespace, pods = driver.pods(platform)
    require(driver.namespace(platform)["metadata"]["uid"] == driver.bound[platform][0] and probe["platform_id"] == platform and
            probe["platform_revision"] == current["revision"] and probe["namespace_uid"] == driver.bound[platform][0], "target snapshot scope changed")
    marker = driver.proxy_query(platform, driver.proxy_password(platform), "SELECT v FROM hakopod_native_acceptance WHERE k='backup';")
    require(marker == "backup-" + driver.run_id, "target data changed before partial-create cleanup")
    return {"revision": current["revision"], "namespace_uid": driver.bound[platform][0],
            **{key: probe[key] for key in ("tenant_id", "timeline_id", "tenant_generation", "timeline_generation")},
            "pod_uids": sorted(p["metadata"]["uid"] for p in pods), "data_sha256": digest(marker.encode())}


def volume_inventory(driver):
    values = json.loads(driver.k("get", "pv", "--chunk-size=128", "-o", "json"))["items"]
    require(len(values) <= 512, "native persistent volume inventory exceeds its bound")
    return values


def validate_observation(value, resources):
    source, target = resources["source"], resources["recovery_target"]
    require(set(value) == PREPARATION_FIELDS | AFTER_FIELDS and value["platform_id"] == source["platform_id"] and
            value["namespace_uid"] == source["namespace_uid"] and value["target_platform_id"] == target["platform_id"] and
            value["target_namespace_uid"] == target["namespace_uid"] and value["platform_id"] != value["target_platform_id"],
            "partial-create observation scope differs")
    require(all(ID.fullmatch(str(value[k])) for k in ("run_id", "tenant_id", "timeline_id", "ancestor_timeline_id")) and
            value["timeline_id"] != value["ancestor_timeline_id"] and type(value["source_revision"]) is int and value["source_revision"] > 0,
            "partial-create observed identities differ")
    unused, normalized, request_hash = branch_request(value["tenant_id"], value["timeline_id"], value["ancestor_timeline_id"])
    hashes = ("normalized_request_sha256", "controller_request_hash", "ownership_token_sha256", "create_error_sha256",
              "reservation_before_sha256", "prior_lifecycle_sha256", "backup_recovery_sha256", "preparation_sha256")
    require(all(DIGEST.fullmatch(str(value[k])) for k in hashes) and value["normalized_request_sha256"] == normalized and
            value["controller_request_hash"] == request_hash and type(value["create_http_status"]) is int and value["create_http_status"] == 500,
            "partial-create request or failure witness differs")
    expected = {"resource_kind": "timeline", "external_key": value["tenant_id"] + "/" + value["timeline_id"], "state": "applying", "result": None,
                "request_hash": request_hash, "ownership_token_sha256": value["ownership_token_sha256"], "timeline_rows": 0}
    require(value["ledger_before"] == expected and type(value["ledger_before"]["timeline_rows"]) is int,
            "partial-create unfinished ownership witness differs")
    deletion = value["delete_operation"]
    require(set(deletion) == {"id", "platform_id", "kind", "status", "revision"} and ID.fullmatch(str(deletion["id"])) and
            deletion["platform_id"] == value["platform_id"] and deletion["kind"] == "delete" and deletion["status"] == "succeeded" and
            type(deletion["revision"]) is int and deletion["revision"] == value["source_revision"] + 1,
            "partial-create parent API delete witness differs")
    tombstone, fence = value["tombstone"], value["tenant_fence"]
    require(set(tombstone) == {"schema_version", "ownership_token_sha256", "request_hash", "body_sha256"} and
            type(tombstone["schema_version"]) is int and tombstone["schema_version"] == 1 and
            tombstone["ownership_token_sha256"] == value["ownership_token_sha256"] and tombstone["request_hash"] == request_hash and
            DIGEST.fullmatch(str(tombstone["body_sha256"])) and
            set(fence) == {"schema_version", "delete_token_sha256", "authorization_digest", "body_sha256"} and
            type(fence["schema_version"]) is int and fence["schema_version"] == 1 and
            all(DIGEST.fullmatch(str(fence[k])) for k in ("delete_token_sha256", "authorization_digest", "body_sha256")),
            "partial-create deletion fence witness differs")
    foreign = value["foreign_authority"]
    require(set(foreign) == {"source_bearer_http_status", "source_owner_delete_http_status", "validate_only"} and
            foreign["source_bearer_http_status"] in (401, 403) and foreign["source_owner_delete_http_status"] == 409 and foreign["validate_only"] is True,
            "partial-create cross-platform authority was not refused")
    before = value["target_before"]
    require(value["target_after"] == before and set(before) == {"revision", "namespace_uid", "tenant_id", "timeline_id", "tenant_generation", "timeline_generation", "pod_uids", "data_sha256"} and
            before["namespace_uid"] == target["namespace_uid"] and all(type(before[k]) is int and before[k] > 0 for k in ("revision", "tenant_generation", "timeline_generation")) and
            all(ID.fullmatch(str(before[k])) for k in ("tenant_id", "timeline_id")) and before["tenant_id"] != value["tenant_id"] and
            before["data_sha256"] == digest(("backup-" + value["run_id"]).encode()) and
            isinstance(before["pod_uids"], list) and 1 <= len(before["pod_uids"]) <= 32 and len(set(before["pod_uids"])) == len(before["pod_uids"]),
            "another admitted target was not preserved")
    volumes = value["source_persistent_volume_ids"]
    require(isinstance(volumes, list) and 1 <= len(volumes) <= 32 and all(set(p) == {"name", "uid"} and all(isinstance(v, str) and v for v in p.values()) for p in volumes) and
            len({p["uid"] for p in volumes}) == len(volumes) and all(type(value[k]) is int and value[k] == 0 for k in ("data_prefix_objects_before", "data_prefix_objects_after")) and
            value["source_namespace_absent"] is True and value["source_persistent_volumes_absent"] is True,
            "partial-create owned data or volume cleanup is incomplete")


def validate_supporting_files(observation_path, value):
    root = Path(observation_path).parent
    def read(name, maximum=LIMIT):
        path = root / name
        require(not path.is_symlink() and path.is_file() and not path.stat().st_mode & 0o077 and path.stat().st_size <= maximum,
                "partial-create witness file is not protected and bounded")
        return path.read_bytes()
    preparation = read("partial-create-preparation.json")
    require(digest(preparation) == value["preparation_sha256"] and json.loads(preparation) == {k: value[k] for k in PREPARATION_FIELDS},
            "partial-create preparation witness changed")
    request = read("partial-create-request.json", 4096)
    expected, unused, unused_hash = branch_request(value["tenant_id"], value["timeline_id"], value["ancestor_timeline_id"])
    require(json.loads(request) == expected and digest(request) == value["normalized_request_sha256"], "partial-create normalized request witness changed")
    error = read("partial-create-error.body")
    require(digest(error) == value["create_error_sha256"] and missing_ancestor_error(value["create_http_status"], error),
            "partial-create real failure witness changed")
    lifecycle = read("source-lifecycle-before-partial-create.json")
    prior = json.loads(lifecycle)
    require(digest(lifecycle) == value["prior_lifecycle_sha256"] and prior["run_id"] == value["run_id"] and
            prior["platform_id"] == value["platform_id"] and prior["namespace_uid"] == value["namespace_uid"] and prior["tenant_id"] == value["tenant_id"] and
            prior["timeline_id"] not in (value["timeline_id"], value["ancestor_timeline_id"]) and prior["created"] is True and prior["deleted"] is False,
            "prior source lifecycle witness changed")
    backup = read("backup-recovery.json")
    require(digest(backup) == value["backup_recovery_sha256"] and json.loads(backup)["platform_id"] == value["platform_id"],
            "prior source recovery witness changed")


def run(driver, source, target, atomic):
    pid, target_id = source["id"], target["id"]
    require(pid != target_id and driver.pending_lifecycle is not None, "partial-create case requires two admitted platforms")
    current = driver.api("GET", "/api/v1/managed-platforms/" + pid)
    probe = driver.api("POST", "/api/v1/managed-platforms/" + pid + "/native-probe", {"expected_revision": current["revision"]})
    require(probe["platform_id"] == pid and probe["namespace_uid"] == driver.bound[pid][0] and probe["platform_revision"] == current["revision"], "partial-create source scope changed")
    tenant, timeline, ancestor, token = probe["tenant_id"], secrets.token_hex(16), secrets.token_hex(16), secrets.token_hex(16)
    require(len({timeline, ancestor, probe["timeline_id"], token}) == 4, "partial-create identifiers are not fresh")
    body, normalized, request_hash = branch_request(tenant, timeline, ancestor)
    token_hash = digest(token.encode())
    before_target = target_snapshot(driver, target_id)
    owned_volumes = sorted([{"name": p["metadata"]["name"], "uid": p["metadata"]["uid"]} for p in volume_inventory(driver)
                            if p.get("spec", {}).get("claimRef", {}).get("namespace") == "managed-platform-" + pid], key=lambda p: p["name"])
    require(1 <= len(owned_volumes) <= 32, "source persistent volume ownership is missing or unbounded")
    prior_path = driver.evidence / "source-lifecycle-before-partial-create.json"
    atomic(prior_path, driver.pending_lifecycle)
    backup = driver.evidence / "backup-recovery.json"
    require(backup.is_file(), "source recovery evidence is missing")
    objects = Objects(driver, pid, current["spec"])
    data_path = "tenants/" + tenant + "/timelines/" + timeline + "/"
    reservation_path = "hakopod-ownership-v1/timeline-reservations/" + tenant + "/" + timeline + ".reservation.json"
    tombstone_path = "hakopod-ownership-v1/timeline-tombstones/" + tenant + "/" + timeline + ".tombstone.json"
    require(ledger(driver, pid, tenant, timeline) == [] and objects.marker(reservation_path, optional=True) is None and
            objects.marker(tombstone_path, optional=True) is None, "partial-create timeline ownership is not fresh")
    objects.empty(data_path)
    with Controller(driver, pid) as controller:
        status, tenant_raw = controller.request("GET", "/control/v1/tenant/" + tenant)
        tenant_info = json.loads(tenant_raw)
        require(status == 200 and ID.fullmatch(tenant_info.get("ownership_token", "")), "source tenant ownership was unavailable")
        source_owner, source_bearer = tenant_info["ownership_token"], controller.bearer
        for identity in (ancestor, timeline):
            status, unused = controller.request("GET", "/control/v1/tenant/" + tenant + "/timeline/" + identity)
            require(status == 404, "partial-create ancestor or timeline already exists")
        status, error = controller.request("POST", "/v1/tenant/" + tenant + "/timeline", body, owner=token)
        require(missing_ancestor_error(status, error), "owned branch did not fail for its missing ancestor")
        create_status, error_hash = status, digest(error)
        preserve_raw(driver.evidence / "partial-create-request.json", encoded(body))
        preserve_raw(driver.evidence / "partial-create-error.body", error)
    observed = ledger(driver, pid, tenant, timeline)
    expected = {"resource_kind": "timeline", "external_key": tenant + "/" + timeline, "state": "applying", "result": None,
                "request_hash": request_hash, "ownership_token_sha256": token_hash, "timeline_rows": 0}
    require(observed == [expected], "failed branch did not retain exact unfinished provider authority")
    before_count = objects.empty(data_path)
    reservation = objects.marker(reservation_path)
    require(reservation[0] == {"schema_version": 1, "token": token, "request_hash": request_hash}, "partial branch reservation differs")
    preparation = {"run_id": driver.run_id, "platform_id": pid, "namespace_uid": driver.bound[pid][0], "target_platform_id": target_id,
        "target_namespace_uid": driver.bound[target_id][0], "source_revision": current["revision"], "tenant_id": tenant, "timeline_id": timeline, "ancestor_timeline_id": ancestor,
        "normalized_request_sha256": normalized, "controller_request_hash": request_hash, "ownership_token_sha256": token_hash,
        "create_http_status": create_status, "create_error_sha256": error_hash, "ledger_before": expected,
        "data_prefix_objects_before": before_count, "reservation_before_sha256": reservation[1], "source_persistent_volume_ids": owned_volumes,
        "target_before": before_target, "prior_lifecycle_sha256": digest(prior_path.read_bytes()), "backup_recovery_sha256": digest(backup.read_bytes())}
    preparation_path = driver.evidence / "partial-create-preparation.json"
    atomic(preparation_path, preparation)
    deletion = driver.delete(pid, current["spec"]["name"], 600)
    driver.deleted_platforms.add(pid)
    require(deletion["platform_id"] == pid and deletion["kind"] == "delete" and deletion["status"] == "succeeded", "parent API deletion did not succeed")
    deadline = time.monotonic() + 120
    owned_ids = {p["uid"] for p in owned_volumes}
    while time.monotonic() < deadline:
        present = driver.k("get", "namespace", "managed-platform-" + pid, "--ignore-not-found", "-o", "name").strip()
        pvs = volume_inventory(driver)
        if not present and not any(p["metadata"]["uid"] in owned_ids or p.get("spec", {}).get("claimRef", {}).get("namespace") == "managed-platform-" + pid for p in pvs):
            break
        time.sleep(2)
    else:
        raise RuntimeError("source namespace or owned persistent volumes remain after API deletion")
    tombstone, tombstone_sha = objects.marker(tombstone_path)
    require(tombstone == {"schema_version": 1, "token": token, "request_hash": request_hash}, "partial timeline tombstone does not match real request authority")
    fence, fence_sha = objects.marker("hakopod-ownership-v1/tenant-deletions/" + tenant + ".json")
    require(set(fence) == {"schema_version", "delete_token", "authorization_digest"} and fence["schema_version"] == 1 and
            ID.fullmatch(fence["delete_token"]) and DIGEST.fullmatch(fence["authorization_digest"]), "parent deletion fence is malformed")
    after_count = objects.empty(data_path)
    with Controller(driver, target_id) as controller:
        bearer_status, unused = controller.request("GET", "/control/v1/tenant/" + before_target["tenant_id"], bearer=source_bearer)
        require(bearer_status in (401, 403), "deleted source bearer reached another platform")
        owner_status, unused = controller.request("DELETE", "/v1/tenant/" + before_target["tenant_id"] + "?validate_only=true", owner=source_owner, deletion=fence["delete_token"])
        require(owner_status == 409, "deleted source authority reached another tenant")
    after_target = target_snapshot(driver, target_id)
    require(after_target == before_target, "another admitted platform changed during source deletion")
    observation = dict(preparation, preparation_sha256=digest(preparation_path.read_bytes()), data_prefix_objects_after=after_count,
        delete_operation={k: deletion[k] for k in ("id", "platform_id", "kind", "status", "revision")},
        tombstone={"schema_version": 1, "ownership_token_sha256": token_hash, "request_hash": request_hash, "body_sha256": tombstone_sha},
        tenant_fence={"schema_version": 1, "delete_token_sha256": digest(fence["delete_token"].encode()), "authorization_digest": fence["authorization_digest"], "body_sha256": fence_sha},
        foreign_authority={"source_bearer_http_status": bearer_status, "source_owner_delete_http_status": owner_status, "validate_only": True},
        target_after=after_target, source_namespace_absent=True, source_persistent_volumes_absent=True)
    driver.record("partial-create-cleanup", observation)
    return observation

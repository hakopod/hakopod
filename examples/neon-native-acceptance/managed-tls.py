#!/usr/bin/env python3
"""Disposable-cluster Neon managed TLS renewal qualification helper."""
import argparse
import base64
import hashlib
import json
import os
import pathlib
import re
import secrets
import socket
import ssl
import struct
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

ID = re.compile(r"^[0-9a-f]{32}$")
UID = re.compile(r"^[0-9a-f-]{8,64}$")
NAME = re.compile(r"^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$")
DIGEST = re.compile(r"^[0-9a-f]{64}$")
SNAPSHOT = re.compile(r"^platform-tls-proxy-[0-9a-f]{16}-r1$")
LOGICAL = (
    "broker-auth", "compute-auth", "controller-auth",
    "controller-database-password", "pageserver-auth", "proxy-auth",
    "safekeeper-auth",
)
SHAPES = {
    "broker-auth": {"tls.crt", "tls.key", "ca.crt"},
    "compute-auth": {"config.json", "token", "tls.crt", "tls.key", "ca.crt"},
    "controller-auth": {"token", "public-key.pem", "tls.crt", "tls.key", "ca.crt"},
    "controller-database-password": {"value", "tls.crt", "tls.key", "ca.crt"},
    "pageserver-auth": {"token", "public-key.pem", "tls.crt", "tls.key", "ca.crt"},
    "proxy-auth": {"token", "tls.crt", "tls.key"},
    "safekeeper-auth": {"token", "public-key.pem", "tls.crt", "tls.key", "ca.crt"},
}


def run(argv, *, data=None, timeout=30, env=None):
    if not isinstance(argv, list) or not 1 <= len(argv) <= 64:
        raise RuntimeError("command is invalid")
    process = subprocess.run(argv, input=data, stdout=subprocess.PIPE,
                             stderr=subprocess.PIPE, timeout=timeout, env=env)
    if process.returncode:
        raise RuntimeError(f"command failed: {pathlib.Path(argv[0]).name}")
    if len(process.stdout) > 8 << 20 or len(process.stderr) > 8 << 20:
        raise RuntimeError("command output exceeded bound")
    return process.stdout


def kube(a, *args, **kwargs):
    return run([a.kubectl, "--kubeconfig", a.kubeconfig, "--context",
                "k3d-hakopod-dev", *args], **kwargs)


def protected_argv(path):
    item = pathlib.Path(path)
    info = item.lstat()
    if item.is_symlink() or not item.is_file() or info.st_mode & 0o077 or not 1 < info.st_size <= 8192:
        raise RuntimeError("protected psql command is invalid")
    value = json.loads(item.read_text())
    if (not isinstance(value, list) or not 1 <= len(value) <= 32 or
            any(not isinstance(part, str) or not part or "\x00" in part or len(part) > 4096 for part in value)):
        raise RuntimeError("psql command must be a bounded JSON argv array")
    return value


def psql(a, sql):
    raw = run(a.psql_argv, data=(sql.rstrip() + "\n").encode(), timeout=30).decode()
    values = [line.removeprefix("HAKOPOD_RESULT:") for line in raw.splitlines()
              if line.startswith("HAKOPOD_RESULT:")]
    if len(values) != 1:
        raise RuntimeError("control database result marker is missing")
    return values[0]


def obj(a, kind, name, namespace=None):
    prefix = [] if namespace is None else ["-n", namespace]
    return json.loads(kube(a, *prefix, "get", kind, name, "-o", "json"))


def api(a, path):
    token = pathlib.Path(a.api_token_file).read_text().strip()
    request = urllib.request.Request(a.api_url + "/api/v1" + path,
                                     headers={"Authorization": "Bearer " + token})
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            raw = response.read((1 << 20) + 1)
    except urllib.error.HTTPError as error:
        error.read((1 << 20) + 1)
        raise RuntimeError("API request was refused") from error
    if len(raw) > 1 << 20:
        raise RuntimeError("API response exceeded bound")
    return json.loads(raw)


def sha(value):
    return hashlib.sha256(value).hexdigest()


def cert_fingerprint(pem):
    return sha(ssl.PEM_cert_to_DER_cert(pem.decode()))


def managed_name(logical):
    if logical == "controller-database-password":
        return "controller-db"
    return logical.removesuffix("-auth")


def snapshot_name(logical, data):
    encoded = {key: base64.b64encode(value).decode() for key, value in data.items()}
    digest = sha(json.dumps(encoded, sort_keys=True, separators=(",", ":")).encode())[:16]
    return f"platform-tls-{managed_name(logical)}-{digest}-r1"


def validate_owned(meta, a, namespace_uid):
    labels = meta.get("labels", {})
    owners = meta.get("ownerReferences", [])
    if (labels.get("app.kubernetes.io/managed-by") != "hakopod" or
            labels.get("hakopod.io/managed-platform-id") != a.platform_id):
        raise RuntimeError("object labels are not owned")
    if (len(owners) != 1 or owners[0].get("kind") != "Namespace" or
            owners[0].get("name") != a.namespace or owners[0].get("uid") != namespace_uid):
        raise RuntimeError("object namespace owner differs")


def current_platform(a):
    value = api(a, "/managed-platforms/" + a.platform_id)
    if (value.get("id") != a.platform_id or value.get("project") != a.project or
            value.get("environment") != a.environment or value.get("revision") != int(a.platform_revision) or
            value.get("spec", {}).get("kind") != "neon" or value["spec"].get("tls_mode") != "managed"):
        raise RuntimeError("managed Neon platform identity differs")
    return value


def trust(a):
    value = api(a, f"/managed-platforms/{a.platform_id}/trust?project={a.project}&environment={a.environment}")
    if (set(value) != {"certificate_pem", "fingerprint", "issuer", "not_before", "expires_at"} or
            not DIGEST.fullmatch(value.get("fingerprint", "")) or
            "PRIVATE KEY" in value.get("certificate_pem", "")):
        raise RuntimeError("managed trust response is invalid")
    return value


def active_inventory(a):
    current_platform(a)
    namespace = obj(a, "namespace", a.namespace)
    namespace_uid = namespace["metadata"]["uid"]
    deployment = obj(a, "deployment", "neon-proxy", a.namespace)
    validate_owned(deployment["metadata"], a, namespace_uid)
    if deployment["metadata"].get("deletionTimestamp"):
        raise RuntimeError("Neon proxy is deleting")
    workloads = json.loads(kube(a, "-n", a.namespace, "get", "deployments,statefulsets",
                                "-l", "hakopod.io/managed-platform-id=" + a.platform_id, "-o", "json"))
    if not 1 <= len(workloads.get("items", [])) <= 32:
        raise RuntimeError("Neon workload inventory is incomplete or unbounded")
    references = {logical: set() for logical in LOGICAL}
    for workload in workloads["items"]:
        for item in workload.get("spec", {}).get("template", {}).get("spec", {}).get("volumes", []):
            if item.get("name") in references and item.get("secret", {}).get("secretName"):
                references[item["name"]].add(item["secret"]["secretName"])
    snapshots = {}
    for logical in LOGICAL:
        if len(references[logical]) != 1:
            raise RuntimeError(f"active Neon TLS reference {logical} is ambiguous")
        name = next(iter(references[logical]))
        secret = obj(a, "secret", name, a.namespace)
        validate_owned(secret["metadata"], a, namespace_uid)
        data = {key: base64.b64decode(value, validate=True) for key, value in secret.get("data", {}).items()}
        if (secret.get("immutable") is not True or set(data) != SHAPES[logical] or
                snapshot_name(logical, data) != name):
            raise RuntimeError(f"managed TLS snapshot {logical} is invalid")
        snapshots[logical] = (secret, data)
    proxy_name = snapshots["proxy-auth"][0]["metadata"]["name"]
    matches = [volume for volume in deployment["spec"]["template"]["spec"].get("volumes", [])
               if volume.get("secret", {}).get("secretName") == proxy_name]
    if len(matches) != 1 or not NAME.fullmatch(matches[0].get("name", "")) or not SNAPSHOT.fullmatch(proxy_name):
        raise RuntimeError("Neon proxy TLS volume is ambiguous")
    issuer = obj(a, "secret", "platform-tls-issuer", a.namespace)
    validate_owned(issuer["metadata"], a, namespace_uid)
    if issuer.get("immutable") is True or set(issuer.get("data", {})) != {"ca.crt", "ca.key"}:
        raise RuntimeError("managed issuer shape is invalid")
    return namespace, deployment, issuer, snapshots, matches[0]["name"]


def verify_claims(a, namespace, deployment, issuer, snapshots):
    rows = [("namespace." + a.namespace, namespace["metadata"]["uid"], 1),
            ("secret.platform-tls-issuer", issuer["metadata"]["uid"], 1),
            ("deployment.neon-proxy", deployment["metadata"]["uid"], int(deployment["metadata"]["generation"]))]
    rows.extend(("secret." + secret["metadata"]["name"], secret["metadata"]["uid"], 1)
                for secret, _ in snapshots.values())
    wanted = " UNION ALL ".join("SELECT '%s'::text,'%s'::text,%d::bigint" % row for row in rows)
    sql = (f"WITH wanted(component,resource_id,generation) AS ({wanted}), found AS (SELECT count(*) n FROM wanted w JOIN platform_component_resources r ON "
           f"r.platform_id='{a.platform_id}' AND r.platform_revision={int(a.platform_revision)} AND r.component=w.component AND r.resource_kind='runtime_component' AND "
           f"r.resource_id=w.resource_id AND r.immutable_generation=w.generation AND r.owner_operation_id='{a.owner_operation_id}' AND r.released_at IS NULL) "
           f"SELECT 'HAKOPOD_RESULT:'||n FROM found;")
    if psql(a, sql) != str(len(rows)):
        raise RuntimeError("durable Neon TLS ownership claims differ")


def validate_certificates(a, issuer, snapshots):
    issuer_ca = base64.b64decode(issuer["data"]["ca.crt"], validate=True)
    trust_value = trust(a)
    if cert_fingerprint(issuer_ca) != trust_value["fingerprint"] or issuer_ca != trust_value["certificate_pem"].encode():
        raise RuntimeError("trust API and issuer CA differ")
    result = {}
    with tempfile.TemporaryDirectory(prefix="hakopod-neon-tls-public-", dir=a.private_tmp) as directory:
        os.chmod(directory, 0o700)
        root = pathlib.Path(directory)
        (root / "ca.crt").write_bytes(issuer_ca)
        for logical, (_, data) in snapshots.items():
            cert = root / (managed_name(logical) + ".crt")
            cert.write_bytes(data["tls.crt"])
            run([a.openssl, "x509", "-in", str(cert), "-checkend", "0", "-noout"])
            run([a.openssl, "verify", "-CAfile", str(root / "ca.crt"), str(cert)])
            if logical != "proxy-auth" and data["ca.crt"] != issuer_ca:
                raise RuntimeError(f"managed TLS snapshot {logical} has a different CA")
            result[logical] = {"snapshot_name": snapshots[logical][0]["metadata"]["name"],
                               "snapshot_uid": snapshots[logical][0]["metadata"]["uid"],
                               "leaf_fingerprint": cert_fingerprint(data["tls.crt"]),
                               "valid_beyond_renewal_window": subprocess.run(
                                   [a.openssl, "x509", "-in", str(cert), "-checkend", str(7 * 24 * 60 * 60), "-noout"],
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10).returncode == 0}
    return issuer_ca, result


def served_proxy(a, ca_path):
    raw = socket.create_connection(("127.0.0.1", a.local_port), timeout=5)
    try:
        raw.sendall(struct.pack("!II", 8, 80877103))
        if raw.recv(1) != b"S":
            raise RuntimeError("Neon proxy refused PostgreSQL TLS negotiation")
        context = ssl.create_default_context(cafile=ca_path)
        with context.wrap_socket(raw, server_hostname=a.proxy_host) as connection:
            return sha(connection.getpeercert(binary_form=True))
    finally:
        raw.close()


def observe(a):
    namespace, deployment, issuer, snapshots, volume = active_inventory(a)
    verify_claims(a, namespace, deployment, issuer, snapshots)
    issuer_ca, inventory = validate_certificates(a, issuer, snapshots)
    pathlib.Path(a.ca_file).write_bytes(issuer_ca)
    os.chmod(a.ca_file, 0o600)
    served = served_proxy(a, a.ca_file)
    if served != inventory["proxy-auth"]["leaf_fingerprint"]:
        raise RuntimeError("Neon proxy is not serving its claimed snapshot")
    return {"status": "passed", "tls_mode": "managed", "namespace_uid": namespace["metadata"]["uid"],
            "deployment_uid": deployment["metadata"]["uid"], "deployment_generation": deployment["metadata"]["generation"],
            "proxy_volume": volume, "ca_fingerprint": cert_fingerprint(issuer_ca),
            "served_proxy_leaf_fingerprint": served, "snapshots": inventory}


def inject(a):
    namespace, deployment, issuer, snapshots, volume = active_inventory(a)
    verify_claims(a, namespace, deployment, issuer, snapshots)
    issuer_ca, inventory = validate_certificates(a, issuer, snapshots)
    old_secret, old_data = snapshots["proxy-auth"]
    data = dict(old_data)
    with tempfile.TemporaryDirectory(prefix="hakopod-neon-managed-tls-", dir=a.private_tmp) as directory:
        os.chmod(directory, 0o700)
        root = pathlib.Path(directory)
        (root / "ca.crt").write_bytes(issuer_ca)
        (root / "ca.key").write_bytes(base64.b64decode(issuer["data"]["ca.key"], validate=True))
        os.chmod(root / "ca.key", 0o600)
        run([a.openssl, "ecparam", "-name", "prime256v1", "-genkey", "-noout", "-out", str(root / "tls.key")])
        run([a.openssl, "req", "-new", "-key", str(root / "tls.key"), "-subj", "/CN=neon-proxy", "-out", str(root / "leaf.csr")])
        names = [a.proxy_host, "neon-proxy", f"neon-proxy.{a.namespace}.svc", f"neon-proxy.{a.namespace}.svc.cluster.local"]
        names = sorted(set(names))
        (root / "ext").write_text("basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=serverAuth\nsubjectAltName=" + ",".join("DNS:" + name for name in names) + "\n")
        run([a.openssl, "x509", "-req", "-in", str(root / "leaf.csr"), "-CA", str(root / "ca.crt"),
             "-CAkey", str(root / "ca.key"), "-set_serial", str(int(time.time() * 1000000)), "-days", "2",
             "-extfile", str(root / "ext"), "-out", str(root / "tls.crt")])
        data["tls.crt"] = (root / "tls.crt").read_bytes()
        data["tls.key"] = (root / "tls.key").read_bytes()
    if set(data) != SHAPES["proxy-auth"]:
        raise RuntimeError("injected proxy snapshot shape differs")
    name = snapshot_name("proxy-auth", data)
    manifest = {"apiVersion": "v1", "kind": "Secret", "metadata": {"name": name, "namespace": a.namespace,
                "labels": old_secret["metadata"]["labels"], "ownerReferences": old_secret["metadata"]["ownerReferences"]},
                "immutable": True, "type": old_secret.get("type", "Opaque"),
                "data": {key: base64.b64encode(value).decode() for key, value in data.items()}}
    kube(a, "create", "-f", "-", data=json.dumps(manifest).encode())
    created = obj(a, "secret", name, a.namespace)
    validate_owned(created["metadata"], a, namespace["metadata"]["uid"])
    if not UID.fullmatch(created["metadata"]["uid"]):
        raise RuntimeError("created proxy snapshot UID is invalid")
    claim_sql = ("BEGIN; SELECT pg_advisory_xact_lock(hashtextextended('" + a.platform_id + "',691)); "
                 "INSERT INTO platform_component_resources(platform_id,platform_revision,component,resource_kind,resource_id,immutable_generation,owner_operation_id) VALUES "
                 f"('{a.platform_id}',{int(a.platform_revision)},'secret.{name}','runtime_component','{created['metadata']['uid']}',1,'{a.owner_operation_id}'); "
                 "SELECT 'HAKOPOD_RESULT:claimed'; COMMIT;")
    if psql(a, claim_sql) != "claimed":
        raise RuntimeError("injected proxy snapshot claim was not recorded")
    token = secrets.token_hex(16)
    old_generation = int(deployment["metadata"]["generation"])
    patch = {"metadata": {"uid": deployment["metadata"]["uid"], "resourceVersion": deployment["metadata"]["resourceVersion"],
             "annotations": {"hakopod.io/acceptance-near-expiry": name, "hakopod.io/runtime-transition": token}},
             "spec": {"template": {"spec": {"volumes": [{"name": volume, "secret": {"secretName": name}}]}}}}
    patch_text = json.dumps(patch, separators=(",", ":"))
    normalized = json.loads(kube(a, "-n", a.namespace, "patch", "deployment", "neon-proxy", "--type=strategic",
                                 "--dry-run=server", "-o", "json", "-p", patch_text))
    new_generation = int(normalized["metadata"]["generation"])
    spec_digest = run([a.spec_digest_helper], data=json.dumps(normalized, separators=(",", ":")).encode()).decode().strip()
    if new_generation != old_generation + 1 or not DIGEST.fullmatch(spec_digest):
        raise RuntimeError("normalized Neon proxy transition is invalid")
    component = "deployment.neon-proxy"
    deployment_uid = deployment["metadata"]["uid"]
    prepare = ("WITH prepared AS (INSERT INTO managed_platform_runtime_mutations(platform_id,platform_revision,component,operation_id,resource_id,old_generation,new_generation,transition_token,spec_sha256) "
               f"SELECT platform_id,platform_revision,component,'{a.owner_operation_id}',resource_id,immutable_generation,{new_generation},'{token}','{spec_digest}' FROM platform_component_resources "
               f"WHERE platform_id='{a.platform_id}' AND platform_revision={int(a.platform_revision)} AND component='{component}' AND resource_kind='runtime_component' AND resource_id='{deployment_uid}' AND immutable_generation={old_generation} AND owner_operation_id='{a.owner_operation_id}' AND released_at IS NULL "
               "ON CONFLICT(platform_id,platform_revision,component) DO UPDATE SET resource_id=EXCLUDED.resource_id,old_generation=EXCLUDED.old_generation,new_generation=EXCLUDED.new_generation,transition_token=EXCLUDED.transition_token,spec_sha256=EXCLUDED.spec_sha256,completed_at=NULL,created_at=now() WHERE managed_platform_runtime_mutations.completed_at IS NOT NULL AND managed_platform_runtime_mutations.operation_id=EXCLUDED.operation_id RETURNING 1) "
               "SELECT 'HAKOPOD_RESULT:'||count(*) FROM prepared;")
    if psql(a, prepare) != "1":
        raise RuntimeError("Neon proxy transition journal was not prepared")
    kube(a, "-n", a.namespace, "patch", "deployment", "neon-proxy", "--type=strategic", "-p", patch_text)
    updated = obj(a, "deployment", "neon-proxy", a.namespace)
    actual_digest = run([a.spec_digest_helper], data=json.dumps(updated, separators=(",", ":")).encode()).decode().strip()
    if (updated["metadata"]["uid"] != deployment_uid or int(updated["metadata"]["generation"]) != new_generation or
            updated["metadata"].get("annotations", {}).get("hakopod.io/runtime-transition") != token or actual_digest != spec_digest):
        raise RuntimeError("Neon proxy transition differs from its durable intent")
    complete = ("DO $hakopod$ DECLARE changed bigint; BEGIN "
                f"UPDATE platform_component_resources SET immutable_generation={new_generation} WHERE platform_id='{a.platform_id}' AND platform_revision={int(a.platform_revision)} AND component='{component}' AND resource_kind='runtime_component' AND resource_id='{deployment_uid}' AND immutable_generation={old_generation} AND owner_operation_id='{a.owner_operation_id}' AND released_at IS NULL; GET DIAGNOSTICS changed=ROW_COUNT; IF changed<>1 THEN RAISE EXCEPTION 'runtime claim transition mismatch'; END IF; "
                f"UPDATE managed_platform_runtime_mutations SET completed_at=now() WHERE platform_id='{a.platform_id}' AND platform_revision={int(a.platform_revision)} AND component='{component}' AND operation_id='{a.owner_operation_id}' AND resource_id='{deployment_uid}' AND old_generation={old_generation} AND new_generation={new_generation} AND transition_token='{token}' AND spec_sha256='{spec_digest}' AND completed_at IS NULL; GET DIAGNOSTICS changed=ROW_COUNT; IF changed<>1 THEN RAISE EXCEPTION 'runtime journal transition mismatch'; END IF; END $hakopod$; SELECT 'HAKOPOD_RESULT:completed';")
    if psql(a, complete) != "completed":
        raise RuntimeError("Neon proxy transition claim was not completed")
    kube(a, "-n", a.namespace, "rollout", "status", "deployment/neon-proxy", "--timeout=180s", timeout=190)
    pathlib.Path(a.ca_file).write_bytes(issuer_ca)
    os.chmod(a.ca_file, 0o600)
    expected = cert_fingerprint(data["tls.crt"])
    if served_proxy(a, a.ca_file) != expected:
        raise RuntimeError("near-expiry Neon proxy leaf is not served")
    return {"status": "passed", "injected_snapshot": name, "injected_snapshot_uid": created["metadata"]["uid"],
            "prior_snapshot": old_secret["metadata"]["name"], "prior_inventory": inventory,
            "ca_fingerprint": cert_fingerprint(issuer_ca), "near_expiry_leaf_fingerprint": expected,
            "deployment_generation": new_generation, "transition_spec_sha256": spec_digest,
            "transition_journal_complete": True}


def await_renewal(a):
    injected = json.loads(pathlib.Path(a.input).read_text())
    deadline = time.monotonic() + 420
    current = None
    while time.monotonic() < deadline:
        try:
            current = observe(a)
            if current["served_proxy_leaf_fingerprint"] != injected["near_expiry_leaf_fingerprint"]:
                break
        except Exception:
            pass
        time.sleep(3)
    else:
        raise RuntimeError("managed TLS maintenance did not replace the near-expiry Neon proxy leaf")
    if current["ca_fingerprint"] != injected["ca_fingerprint"]:
        raise RuntimeError("managed TLS maintenance changed the Neon platform CA")
    if current["snapshots"]["proxy-auth"]["valid_beyond_renewal_window"] is not True:
        raise RuntimeError("managed TLS maintenance left the Neon proxy leaf inside the seven-day renewal window")
    changes = {logical: current["snapshots"][logical]["snapshot_name"] != injected["prior_inventory"][logical]["snapshot_name"]
               for logical in LOGICAL}
    return {"status": "passed", "ca_fingerprint": current["ca_fingerprint"],
            "prior_proxy_leaf_fingerprint": injected["near_expiry_leaf_fingerprint"],
            "served_proxy_leaf_fingerprint": current["served_proxy_leaf_fingerprint"],
            "reused_prior_proxy_snapshot": current["snapshots"]["proxy-auth"]["snapshot_name"] == injected["prior_snapshot"],
            "snapshot_changed": changes, "snapshots": current["snapshots"], "all_snapshots_owned_and_valid": True}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=("observe", "inject", "await-renewal"))
    for name in ("kubectl", "kubeconfig", "namespace", "platform-id", "platform-revision", "owner-operation-id",
                 "proxy-host", "api-url", "api-token-file", "project", "environment", "ca-file", "private-tmp",
                 "input", "spec-digest-helper", "psql-command-file", "openssl"):
        parser.add_argument("--" + name, default="")
    parser.add_argument("--local-port", type=int, required=True)
    args = parser.parse_args()
    if (args.namespace != "managed-platform-" + args.platform_id or not ID.fullmatch(args.platform_id) or
            not ID.fullmatch(args.owner_operation_id) or not NAME.fullmatch(args.proxy_host) or
            not 1 <= args.local_port <= 65535):
        raise SystemExit("invalid Neon managed TLS fixture scope")
    args.psql_argv = protected_argv(args.psql_command_file)
    result = {"observe": observe, "inject": inject, "await-renewal": await_renewal}[args.mode](args)
    print(json.dumps(result, sort_keys=True, separators=(",", ":")))


if __name__ == "__main__":
    main()

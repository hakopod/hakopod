"""Render the pinned Oracle v4 SIDB schema for isolated qualification.

The Go reconciler installs a scoped controller per database. This payload adds
no shared controller, webhook, ClusterRole or database workload. Release bundle
activation remains held until the operator image and native evidence are pinned.
"""

import copy
import hashlib
import json
import runpy

SOURCE = "ff6f9178c1650df30afbf203ebdb633e9b80760a"
CRD_SHA256 = "379d9c1768ddb7b00eed53ae7a70059ff688bde556798cf60b707b34975d82b6"
CRD_SPEC_SHA256 = "41ff33c11881cb5f4a9fe016c5ea978dbaec245f3a0ae92f33816c5e37844221"
CRD_URL = "https://raw.githubusercontent.com/oracle/oracle-database-operator/" + SOURCE + "/config/crd/bases/database.oracle.com_singleinstancedatabases.yaml"


def validate_objects(objects):
    if not isinstance(objects, list) or len(objects) != 1:
        raise ValueError("Oracle Free installation requires exactly one SIDB resource definition")
    obj = objects[0]
    if not isinstance(obj, dict) or obj.get("apiVersion") != "apiextensions.k8s.io/v1" or obj.get("kind") != "CustomResourceDefinition" or obj.get("metadata", {}).get("name") != "singleinstancedatabases.database.oracle.com":
        raise ValueError("Oracle Free installation accepts only its SIDB definition")
    spec = obj.get("spec", {})
    if spec.get("group") != "database.oracle.com" or spec.get("scope") != "Namespaced" or spec.get("conversion") != {"strategy": "None"}:
        raise ValueError("Oracle Free CRD scope or conversion changed")
    versions = spec.get("versions", [])
    if len(versions) != 1 or versions[0].get("name") != "v4" or versions[0].get("served") is not True or versions[0].get("storage") is not True:
        raise ValueError("Oracle Free requires only the v4 served storage version")
    if hashlib.sha256(json.dumps(spec, sort_keys=True, separators=(",", ":")).encode()).hexdigest() != CRD_SPEC_SHA256:
        raise ValueError("Oracle Free CRD schema differs from its qualified definition")


def render_bytes(raw):
    import yaml
    if len(raw) > 8 * 1024 * 1024 or hashlib.sha256(raw).hexdigest() != CRD_SHA256:
        raise ValueError("Oracle Free CRD source checksum or size changed")
    obj = yaml.safe_load(raw)
    if not isinstance(obj, dict):
        raise ValueError("Oracle Free CRD source is invalid")
    obj = copy.deepcopy(obj)
    spec = obj["spec"]
    spec["versions"] = [version for version in spec["versions"] if version["name"] == "v4"]
    spec["conversion"] = {"strategy": "None"}
    obj["metadata"].pop("annotations", None)
    objects = [obj]
    validate_objects(objects)
    return objects


def render_candidate(scratch, fetch):
    raw = fetch(CRD_URL, CRD_SHA256, scratch / "oracle-sidb-upstream.yaml")
    return render_bytes(raw), {"source": SOURCE, "url": CRD_URL, "sha256": CRD_SHA256}


def qualification(source):
    verifier = runpy.run_path(str(source / "release/verify-oracle-free-runtime.py"))
    directory = source / "release/managed-oracle-free"
    return verifier["validate_metadata"](directory, source), verifier["file_hash"](directory / "manifest.json")


def render(source, scratch, fetch, validate_qualification=qualification):
    manifest, qualification_hash = validate_qualification(source)
    if manifest["source"] != {"repository": "https://github.com/oracle/oracle-database-operator", "revision": SOURCE}:
        raise ValueError("Oracle Free CRD must match the qualified scoped operator source")
    objects, pin = render_candidate(scratch, fetch)
    return objects, {**pin, "operator_image": manifest["images"]["operator"]["reference"],
                     "database_image": manifest["images"]["database"]["reference"],
                     "qualification_sha256": qualification_hash}

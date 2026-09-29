#!/usr/bin/env python3
"""Read-only development probe for documented native Bitbucket API identity.

Reports only allowlisted field shapes, valid UUIDs, and known runner states.
Never stores responses, runner OAuth material, pipeline variables, or commands.
This report is compatibility evidence, not proof of assignment or safe drain.
"""

import argparse
import base64
import json
import os
import re
import stat
import sys
import urllib.error
import urllib.parse
import urllib.request


UUID = re.compile(r"^\{[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\}$")
STATES = {"UNREGISTERED", "ONLINE", "OFFLINE", "DISABLED", "ENABLED", "UNHEALTHY"}
FIELDS = ("uuid", "state", "runner", "current_step", "allocated_step", "step", "pipeline", "repository")


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def unique_object(items):
    result = {}
    for key, value in items:
        if key in result:
            raise ValueError("duplicate JSON field")
        result[key] = value
    return result


def credential(path):
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    descriptor = os.open(path, flags)
    with os.fdopen(descriptor, "rb") as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600 or info.st_uid != os.getuid():
            raise ValueError("credential must be an owned mode-0600 regular file")
        data = stream.read(8193)
    if not data or len(data) > 8192:
        raise ValueError("credential size is invalid")
    value = data.decode("utf-8").strip()
    if value.startswith("{"):
        obj = json.loads(value, object_pairs_hook=unique_object)
        if set(obj) != {"email", "api_token"}:
            raise ValueError("credential JSON requires email and api_token only")
        email, token = obj["email"], obj["api_token"]
        if not isinstance(email, str) or not isinstance(token, str) or not 3 <= len(email) <= 254 or email.count("@") != 1 or ":" in email or not 16 <= len(token) <= 4096:
            raise ValueError("credential shape is invalid")
        if any(ord(char) <= 32 or ord(char) >= 127 for char in email + token):
            raise ValueError("credential encoding is invalid")
        return "Basic " + base64.b64encode((email + ":" + token).encode("ascii")).decode("ascii")
    if not 16 <= len(value) <= 4096 or any(ord(char) <= 32 or ord(char) >= 127 for char in value):
        raise ValueError("OAuth token shape is invalid")
    return "Bearer " + value


def field_shape(value):
    if value is None:
        return {"present": True, "type": "null"}
    if isinstance(value, dict):
        result = {"present": True, "type": "object"}
        identifiers = {}
        for field in ("uuid", "runner_uuid", "step_uuid", "pipeline_uuid", "repository_uuid"):
            candidate = value.get(field)
            if isinstance(candidate, str) and UUID.fullmatch(candidate.lower()):
                identifiers[field] = candidate.lower()
        if identifiers:
            result["identifiers"] = identifiers
        if value.get("status") in STATES:
            result["status"] = value["status"]
        if isinstance(value.get("cordoned"), bool):
            result["cordoned"] = value["cordoned"]
        for field in ("runner", "current_step", "allocated_step", "step", "pipeline"):
            if field in value:
                child = value[field]
                # One-level presence only; untrusted nested objects cannot
                # recursively expand the report or introduce sensitive fields.
                result[field + "_present"] = child is not None
        return result
    if isinstance(value, str) and UUID.fullmatch(value.lower()):
        return {"present": True, "type": "uuid", "uuid": value.lower()}
    return {"present": True, "type": type(value).__name__}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--credential-file", required=True)
    parser.add_argument("--workspace", required=True)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--runner", required=True)
    parser.add_argument("--pipeline")
    parser.add_argument("--step")
    parser.add_argument("--report", required=True)
    args = parser.parse_args()
    for name in ("workspace", "repository", "runner", "pipeline", "step"):
        value = getattr(args, name)
        if value is not None and not UUID.fullmatch(value):
            parser.error(name + " must be a canonical lowercase braced UUID")
    if args.step and not args.pipeline:
        parser.error("step requires pipeline")
    authorization = credential(args.credential_file)
    origin = "https://api.bitbucket.org"
    scope = "/2.0/repositories/" + args.workspace + "/" + args.repository
    paths = {"runner": scope + "/pipelines-config/runners/" + args.runner}
    if args.pipeline:
        paths["pipeline"] = scope + "/pipelines/" + args.pipeline
    if args.step:
        paths["step"] = scope + "/pipelines/" + args.pipeline + "/steps/" + args.step
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    report = {"schema_version": 1, "development_probe": True, "read_only": True, "assignment_verified": False, "drain_verified": False, "resources": {}}
    for kind, path in paths.items():
        request = urllib.request.Request(origin + urllib.parse.quote(path, safe="/"), headers={"Authorization": authorization, "Accept": "application/json"}, method="GET")
        result = {}
        try:
            with opener.open(request, timeout=15) as response:
                data = response.read((1 << 20) + 1)
                if response.status != 200 or len(data) > 1 << 20:
                    raise ValueError("response status or size is invalid")
                obj = json.loads(data, object_pairs_hook=unique_object)
                if not isinstance(obj, dict):
                    raise ValueError("response shape is invalid")
                result = {"status": 200, "fields": {field: field_shape(obj[field]) if field in obj else {"present": False} for field in FIELDS}}
        except urllib.error.HTTPError as error:
            result = {"status": error.code, "error": "redirect refused" if 300 <= error.code < 400 else "provider request rejected"}
            error.close()
        except Exception:
            result = {"error": "bounded provider response could not be verified"}
        report["resources"][kind] = result
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0)
    descriptor = os.open(args.report, flags, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
        json.dump(report, stream, indent=2)
        stream.write("\n")
    print(json.dumps({"probe_written": True, "read_only": True, "resource_count": len(paths), "assignment_verified": False, "drain_verified": False}))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, UnicodeError, json.JSONDecodeError):
        print("Bitbucket schema probe could not validate its private inputs or output path", file=sys.stderr)
        sys.exit(1)

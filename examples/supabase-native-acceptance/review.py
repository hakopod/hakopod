#!/usr/bin/env python3
"""Read an unconsumed server review after preparing its namespace certificates."""
import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import stat


def protected_json(path):
    path = Path(path)
    before = path.lstat()
    if not stat.S_ISREG(before.st_mode) or before.st_uid != os.getuid() or stat.S_IMODE(before.st_mode) != 0o600 or before.st_size > 1 << 20:
        raise ValueError("review and specification must be owned mode-0600 regular files within 1 MiB")
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, "rb") as stream:
        after = os.fstat(stream.fileno())
        def identity(value):
            return (value.st_dev, value.st_ino, value.st_mode, value.st_uid, value.st_gid,
                    value.st_size, value.st_mtime_ns, value.st_ctime_ns)
        if identity(before) != identity(after):
            raise ValueError("review input changed while opening")
        body = stream.read((1 << 20) + 1)
        if identity(after) != identity(os.fstat(stream.fileno())) or identity(after) != identity(path.lstat()):
            raise ValueError("review input changed while reading")
    if len(body) > 1 << 20:
        raise ValueError("review input exceeded 1 MiB")

    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError("duplicate review JSON key")
            result[key] = value
        return result

    return json.loads(body, object_pairs_hook=unique)


def validate_review(document, spec, project, environment, now=None):
    if not isinstance(document, dict) or document.get("blocked") is not False:
        raise ValueError("create review must be available")
    if set(document) != {"blocked", "platform", "review", "plan"}:
        raise ValueError("create review contains unexpected fields")
    platform, review, plan = (document.get(key) for key in ("platform", "review", "plan"))
    if not all(isinstance(value, dict) for value in (platform, review, plan)):
        raise ValueError("create review is incomplete")
    if set(review) - {"id", "kind", "expected_revision", "request_hash", "authority_fingerprint", "capacity_fingerprint", "expires_at", "blocked_reasons"}:
        raise ValueError("create review contains unexpected authority fields")
    platform_id = platform.get("id", "")
    if not isinstance(platform_id, str) or not re.fullmatch(r"[0-9a-f]{32}", platform_id):
        raise ValueError("create review has no server platform identity")
    same_spec = json.dumps(platform.get("spec"), sort_keys=True, separators=(",", ":")) == json.dumps(spec, sort_keys=True, separators=(",", ":"))
    if platform.get("project") != project or platform.get("environment") != environment or not same_spec:
        raise ValueError("create review differs from the requested scope or specification")
    if review.get("kind") != "create" or type(review.get("expected_revision")) is not int or review["expected_revision"] != 0:
        raise ValueError("create review must preserve revision zero")
    if plan.get("namespace") != "managed-platform-" + platform_id:
        raise ValueError("create review namespace differs from its server identity")
    if not isinstance(review.get("id"), str) or not re.fullmatch(r"[0-9a-f]{32}", review["id"]):
        raise ValueError("create review identity is invalid")
    for key in ("request_hash", "authority_fingerprint"):
        if not isinstance(review.get(key), str) or not re.fullmatch(r"[0-9a-f]{64}", review[key]):
            raise ValueError("create review authority is incomplete")
    expiry = review.get("expires_at")
    if not isinstance(expiry, str):
        raise ValueError("create review expiry is invalid")
    expires = datetime.fromisoformat(expiry.replace("Z", "+00:00"))
    current = now or datetime.now(timezone.utc)
    if expires.tzinfo is None or not 0 < (expires - current).total_seconds() <= 610:
        raise ValueError("create review is expired or has an invalid expiry")
    return document


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--review", required=True)
    parser.add_argument("--spec", required=True)
    parser.add_argument("--project", required=True)
    parser.add_argument("--environment", required=True)
    args = parser.parse_args()
    try:
        document = validate_review(protected_json(args.review), protected_json(args.spec), args.project, args.environment)
    except (ValueError, OSError, TypeError, KeyError):
        parser.exit(2, "held create review is invalid; obtain a new server review\n")
    # The API validates these authority fields against its durable review. This
    # helper does not authenticate a review or replace server authorization.
    platform = document["platform"]
    projection = {"blocked": False, "platform": {key: platform[key] for key in ("id", "project", "environment", "spec")}, "review": document["review"]}
    print(json.dumps(projection, sort_keys=True))


if __name__ == "__main__":
    main()

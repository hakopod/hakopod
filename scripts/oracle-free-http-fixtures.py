#!/usr/bin/env python3
"""Owned PostgreSQL and S3 fixture boundary for Oracle Free HTTP acceptance."""

import json
import os
from pathlib import Path
import re
import resource
import secrets
import shutil
import subprocess
import tempfile
import time
from urllib.parse import quote

POSTGRES_IMAGE = "docker.io/library/postgres:17.11-alpine@sha256:18cfe3ef5e6815560c98237d6216d1e5119702fb0f3894c8785dd58b8bbe5d73"
S3_IMAGE = "chrislusf/seaweedfs:4.06@sha256:a064c6923daf4451c943cec2f437a67523d7792ee589089bc4d4c27a61d78dea"
IDENTITY = re.compile(r"^[a-f0-9]{64}$")


def normalized_postgres_digest(reference):
    if "@sha256:" not in reference:
        return None
    repository, digest = reference.rsplit("@sha256:", 1)
    repository = repository.rsplit(":", 1)[0]
    if repository.startswith("docker.io/"):
        repository = repository[len("docker.io/"):]
    if repository.startswith("library/"):
        repository = repository[len("library/"):]
    if repository != "postgres" or not IDENTITY.fullmatch(digest):
        return None
    return "postgres@sha256:" + digest


class Fixtures:
    def __init__(self, output: Path, cache: Path, docker: Path):
        self.output, self.cache, self.docker = map(Path, (output, cache, docker))
        self.state_path = self.output / "fixture-state.json"
        self.receipt_path = self.output / "fixture-cleanup.json"
        self.nonce = secrets.token_hex(16)
        self.pg_name = "hakopod-oracle-http-pg-" + self.nonce[:12]
        self.pg_cidfile = self.output / "postgres.cid"
        self.s3_cidfile = self.output / "s3.cid"
        self.s3_state = self.output / "s3-intent.json"
        self.tmp_root = self.cache / ("oracle-http-" + self.nonce)
        self.credential_file = self.tmp_root / "postgres.env"
        self.shim_dir = self.tmp_root / "docker-shim"

    def _run(self, args, *, output=True):
        with tempfile.TemporaryFile() as stdout:
            result = subprocess.run([str(self.docker), *args], stdin=subprocess.DEVNULL,
                                    stdout=stdout if output else subprocess.DEVNULL,
                                    stderr=subprocess.DEVNULL, check=False, timeout=30,
                                    preexec_fn=lambda: resource.setrlimit(resource.RLIMIT_FSIZE, ((64 << 10) + 1, (64 << 10) + 1)))
            stdout.seek(0)
            data = stdout.read((64 << 10) + 1) if output else b""
        if result.returncode:
            raise ValueError("owned fixture Docker operation failed")
        if len(data) > 64 << 10:
            raise ValueError("owned fixture Docker output exceeded its bound")
        return data.decode().strip()

    def _cid(self, path):
        if not path.exists():
            return ""
        info = path.lstat()
        if not path.is_file() or path.is_symlink() or info.st_uid != os.getuid() or info.st_mode & 0o077 or info.st_size > 65:
            raise ValueError("owned fixture cidfile custody changed")
        value = path.read_text().strip()
        if not IDENTITY.fullmatch(value):
            raise ValueError("owned fixture container identity is invalid")
        return value

    def _inspect(self, cid, expected_name, expected_image, component):
        template = '{{json .Id}} {{json .Name}} {{json .Config.Image}} {{json .Config.Labels}}'
        raw = self._run(["inspect", "--format", template, cid])
        parts = raw.split(" ", 3)
        if len(parts) != 4:
            raise ValueError("owned fixture identity response is invalid")
        identity, name, image, labels = (json.loads(item) for item in parts)
        if (identity != cid or name != "/" + expected_name
                or image != expected_image
                or labels.get("com.hakopod.oracle-http") != self.nonce
                or labels.get("com.hakopod.component") != component):
            raise ValueError("owned fixture container identity changed")
        return {"Id": identity, "Name": name, "Image": image, "Labels": labels}

    def _write_state(self, **extra):
        value = {"schema_version": 1, "nonce": self.nonce, "postgres_name": self.pg_name,
                 "postgres_id": self._cid(self.pg_cidfile), "s3_id": self._cid(self.s3_cidfile), **extra}
        self.state_path.write_text(json.dumps(value, sort_keys=True, indent=2) + "\n")
        os.chmod(self.state_path, 0o600)

    def prepare(self):
        self.output.mkdir(mode=0o700, parents=True, exist_ok=True)
        if self.state_path.exists():
            raise ValueError("Oracle HTTP fixture state already exists")
        self.cache.mkdir(mode=0o700, parents=True, exist_ok=True)
        self.tmp_root.mkdir(mode=0o700)
        password = secrets.token_urlsafe(32)
        self.credential_file.write_text(password + "\n")
        os.chmod(self.credential_file, 0o600)
        self._write_state(status="intent")
        digests = json.loads(self._run(["image", "inspect", "--format", "{{json .RepoDigests}}", POSTGRES_IMAGE]))
        canonical = normalized_postgres_digest(POSTGRES_IMAGE)
        if canonical is None or canonical not in {normalized_postgres_digest(item) for item in digests}:
            raise ValueError("pinned PostgreSQL image is not cached")
        self._run(["run", "-d", "--name", self.pg_name, "--cidfile", str(self.pg_cidfile),
                   "--label", "com.hakopod.oracle-http=" + self.nonce, "--label", "com.hakopod.component=postgres",
                   "--pull=never", "--memory=1g", "--memory-swap=1g", "--cpus=1", "--pids-limit=256", "--read-only",
                   "--tmpfs", "/tmp:rw,noexec,nosuid,size=64m", "--tmpfs", "/var/run/postgresql:rw,nosuid,size=16m",
                   "--tmpfs", "/var/lib/postgresql/data:rw,nosuid,size=768m",
                   "--mount", "type=bind,source=" + str(self.credential_file) + ",target=/run/secrets/postgres_password,readonly",
                   "-e", "POSTGRES_PASSWORD_FILE=/run/secrets/postgres_password", "-e", "POSTGRES_USER=hakopod", "-e", "POSTGRES_DB=postgres",
                   "-p", "127.0.0.1::5432", POSTGRES_IMAGE])
        cid = self._cid(self.pg_cidfile)
        self._inspect(cid, self.pg_name, POSTGRES_IMAGE, "postgres")
        port = self._run(["port", cid, "5432/tcp"])
        if not re.fullmatch(r"127\.0\.0\.1:[0-9]{1,5}", port):
            raise ValueError("owned PostgreSQL loopback port is invalid")
        for _ in range(90):
            if subprocess.run([str(self.docker), "exec", cid, "pg_isready", "-U", "hakopod"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5).returncode == 0:
                break
            time.sleep(1)
        else:
            raise ValueError("owned PostgreSQL fixture did not become ready")
        self.shim_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
        shim = self.shim_dir / "docker"
        shim.write_text(_shim_source(str(self.docker), self.nonce, str(self.s3_cidfile), str(self.s3_state), str(self.tmp_root)))
        os.chmod(shim, 0o700)
        self._write_state(status="ready", postgres_port=port)
        return {"HAKOPOD_TEST_DATABASE_URL": "postgres://hakopod:" + quote(password, safe="") + "@" + port + "/postgres?sslmode=disable",
                "PATH": str(self.shim_dir) + ":" + os.environ.get("PATH", ""), "TMPDIR": str(self.tmp_root),
                "HAKOPOD_ORACLE_HTTP_FIXTURE_NONCE": self.nonce}

    def cleanup(self):
        removed = []
        s3_name = ""
        if self.s3_state.exists():
            info = self.s3_state.lstat()
            if self.s3_state.is_symlink() or not self.s3_state.is_file() or info.st_uid != os.getuid() or info.st_mode & 0o077 or info.st_size > 4096:
                raise ValueError("S3 fixture intent custody changed")
            intent = json.loads(self.s3_state.read_text())
            if intent.get("nonce") != self.nonce or intent.get("image") != S3_IMAGE:
                raise ValueError("S3 fixture intent changed")
            s3_name = intent.get("name", "")
        for path, name, image, component in ((self.s3_cidfile, s3_name, S3_IMAGE, "s3"), (self.pg_cidfile, self.pg_name, POSTGRES_IMAGE, "postgres")):
            cid = self._cid(path)
            if not cid:
                found = self._run(["ps", "-aq", "--no-trunc", "--filter", "label=com.hakopod.oracle-http=" + self.nonce,
                                   "--filter", "label=com.hakopod.component=" + component]).splitlines()
                if len(found) > 1 or any(not IDENTITY.fullmatch(item) for item in found):
                    raise ValueError("owned fixture discovery is ambiguous")
                cid = found[0] if found else ""
            if not cid:
                removed.append({"component": component, "container_id": "", "name": name, "image": image, "already_absent": True})
                continue
            try:
                value = self._inspect(cid, name, image, component)
                self._run(["rm", "--force", "--volumes", value["Id"]], output=False)
                if cid in self._run(["ps", "-aq", "--no-trunc"]).splitlines():
                    raise ValueError("owned fixture container remains after removal")
                removed.append({"component": component, "container_id": cid, "name": name, "image": image})
            except ValueError:
                absent = cid not in self._run(["ps", "-aq", "--no-trunc"]).splitlines()
                if not absent:
                    raise
                removed.append({"component": component, "container_id": cid, "name": name, "image": image, "already_absent": True})
        if self.tmp_root.exists(): shutil.rmtree(self.tmp_root)
        components = {item["component"] for item in removed}
        receipt = {"schema_version": 1, "nonce": self.nonce, "removed": removed,
                   "postgres_container_absent": not self.pg_cidfile.exists() or "postgres" in components,
                   "s3_container_absent": not self.s3_cidfile.exists() or "s3" in components,
                   "credential_files_absent": not self.tmp_root.exists()}
        self.receipt_path.write_text(json.dumps(receipt, sort_keys=True, indent=2) + "\n")
        return receipt


def _shim_source(docker, nonce, cidfile, statefile, cache):
    return f'''#!/usr/bin/env python3
import json, os, resource, subprocess, sys, tempfile
REAL={docker!r}; NONCE={nonce!r}; CIDFILE={cidfile!r}; STATE={statefile!r}; CACHE={cache!r}; IMAGE={S3_IMAGE!r}
a=sys.argv[1:]
if a and a[0] == "run":
    if len(a) != 37: raise SystemExit("refusing unexpected S3 fixture")
    fixed={{0:"run",1:"-d",2:"--name",4:"--label",5:"com.hakopod.test=backups",6:"--label",8:"--user",10:"--entrypoint",11:"/usr/bin/weed",12:"--memory=384m",13:"--cpus=1",14:"--pids-limit=128",15:"-p",17:"-e",18:"GODEBUG=fips140=on",19:"-e",20:"GOMEMLIMIT=256MiB",21:"--mount",23:"--mount",25:IMAGE,26:"-logtostderr=true",27:"server",28:"-s3",29:"-s3.port=9000",30:"-s3.config=/etc/seaweedfs/s3.json",31:"-dir=/data",32:"-master.volumePreallocate",33:"-master.volumeSizeLimitMB=8",34:"-volume.max=16",35:"-ip=127.0.0.1",36:"-ip.bind=0.0.0.0"}}
    if any(a[index] != value for index,value in fixed.items()): raise SystemExit("refusing unexpected S3 fixture")
    name, run_id, user, publish = a[3], a[7], a[9], a[16]
    rid=run_id.removeprefix("com.hakopod.test-run=")
    if len(rid) != 32 or any(c not in "0123456789abcdef" for c in rid) or name != "hakopod-backup-smoke-"+rid[:10] or user != str(os.getuid())+":"+str(os.getgid()) or publish != "127.0.0.1::9000": raise SystemExit("refusing malformed S3 fixture")
    mounts=[]
    for value,target,kind,readonly in ((a[22],"/etc/seaweedfs/s3.json","file",True),(a[24],"/data","dir",False)):
        parts=value.split(",")
        if len(parts) != 3+int(readonly) or parts[0] != "type=bind" or parts[2] != "target="+target or readonly and parts[3] != "readonly": raise SystemExit("refusing malformed S3 mount")
        source=parts[1].removeprefix("source="); real=os.path.realpath(source); root=os.path.realpath(CACHE)
        if not parts[1].startswith("source=") or os.path.commonpath((real,root)) != root: raise SystemExit("refusing S3 mount outside scratch")
        st=os.lstat(source)
        if os.path.islink(source) or st.st_uid != os.getuid() or st.st_mode & 0o077 or (kind=="file" and not os.path.isfile(source)) or (kind=="dir" and not os.path.isdir(source)): raise SystemExit("refusing S3 mount custody")
        mounts.append(real)
    with open(STATE,"x") as f: json.dump({{"schema_version":1,"name":name,"image":IMAGE,"nonce":NONCE,"cidfile":CIDFILE}},f); f.write("\\n")
    os.chmod(STATE,0o600)
    a[1:1] = ["--cidfile", CIDFILE, "--label", "com.hakopod.oracle-http="+NONCE, "--label", "com.hakopod.component=s3", "--pull=never", "--memory-swap=384m", "--security-opt=no-new-privileges"]
elif a and a[0] in ("inspect", "port", "logs", "rm"):
    try:
        st=os.lstat(CIDFILE)
        if os.path.islink(CIDFILE) or st.st_uid != os.getuid() or st.st_mode & 0o077 or st.st_size > 65: raise OSError()
        cid=open(CIDFILE).read(66).strip()
    except OSError: raise SystemExit("S3 fixture identity is unavailable")
    if len(cid) != 64 or any(c not in "0123456789abcdef" for c in cid): raise SystemExit("S3 fixture identity is invalid")
    with tempfile.TemporaryFile(mode="w+") as output:
        result=subprocess.run([REAL,"inspect","--format","{{{{.Name}}}}",cid],text=True,stdout=output,stderr=subprocess.DEVNULL,timeout=10,check=False,preexec_fn=lambda: resource.setrlimit(resource.RLIMIT_FSIZE,(257,257)))
        output.seek(0); value=output.read(257)
    if result.returncode or len(value)>256: raise SystemExit("S3 fixture identity lookup failed")
    name=value.strip().lstrip("/")
    shapes=(("port",name,"9000/tcp"), ("logs","--tail","40",name), ("rm","--force","--volumes",cid))
    if a[0] == "inspect":
        expected='{{{{.Id}}}} {{{{index .Config.Labels "com.hakopod.test"}}}} {{{{index .Config.Labels "com.hakopod.test-run"}}}}'
        if tuple(a) != ("inspect","--format",expected,name): raise SystemExit("refusing Docker access outside owned S3 fixture")
    elif tuple(a) not in shapes: raise SystemExit("refusing Docker access outside owned S3 fixture")
else: raise SystemExit("refusing unsupported Docker command")
os.execv(REAL, [REAL, *a])
'''

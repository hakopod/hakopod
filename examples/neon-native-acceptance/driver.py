#!/usr/bin/env python3
"""Fixed Neon native acceptance orchestration; evidence comes only from live probes."""
import argparse, hashlib, importlib.util, json, os, re, resource, signal, socket, ssl, struct, subprocess, sys, tempfile, time, urllib.error, urllib.parse, urllib.request
from pathlib import Path

ID = re.compile(r"^[0-9a-f]{32}$")
DIGEST = re.compile(r"^[0-9a-f]{64}$")
COMPONENTS = {"broker", "compute", "compute-tls", "controller-database", "pageserver", "proxy", "safekeeper", "storage-controller"}
MAX_COMMAND_OUTPUT = 2 << 20
MAX_COMMAND_INPUT = 1 << 20

class TerminationRequested(Exception): pass

def bounded_process_output(output):
    if os.fstat(output.fileno()).st_size > MAX_COMMAND_OUTPUT: raise RuntimeError("Neon client output exceeded its bound")
    return os.pread(output.fileno(),MAX_COMMAND_OUTPUT,0).decode("utf-8", "strict")

def process_output_limit():
    resource.setrlimit(resource.RLIMIT_FSIZE, (MAX_COMMAND_OUTPUT + 1, MAX_COMMAND_OUTPUT + 1))

def cpu_millis(value):
    if not isinstance(value,str) or not re.fullmatch(r"(?:[1-9][0-9]*m|[1-9][0-9]*)",value): raise RuntimeError("Neon acceptance CPU quantity is unsupported")
    return int(value[:-1]) if value.endswith("m") else int(value)*1000

def restart_storage(probe, platform_id, revision, namespace_uid):
    node=probe.get("attached_pageserver_node_id"); component=probe.get("attached_pageserver"); generation=probe.get("tenant_generation"); tenant=probe.get("tenant_id")
    if probe.get("platform_id")!=platform_id or probe.get("platform_revision")!=revision or probe.get("namespace_uid")!=namespace_uid or probe.get("ownership_capability_verified") is not True or type(node) is not int or not 1<=node<=8 or component!="pageserver-"+str(node-1) or type(generation) is not int or generation<1 or not ID.fullmatch(str(tenant)):
        raise RuntimeError("Neon restart active storage identity is not verified")
    return {"component":component,"node_id":node,"tenant_id":tenant,"tenant_generation":generation}

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None

HTTP = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())

def load(path, maximum=2 << 20):
    path = Path(path); info = path.lstat()
    if path.is_symlink() or not path.is_file() or not 1 < info.st_size <= maximum: raise RuntimeError("input is missing, symbolic, or unbounded")
    raw = path.read_bytes()
    if len(raw) > maximum: raise RuntimeError("input exceeded its bound")
    return json.loads(raw)

def protected_text(path, maximum=4096):
    path = Path(path); info = path.lstat()
    if path.is_symlink() or not path.is_file() or info.st_mode & 0o077 or not 0 < info.st_size <= maximum: raise RuntimeError("protected input is missing, symbolic, permissive, or unbounded")
    raw = path.read_bytes()
    if len(raw) > maximum or b"\x00" in raw: raise RuntimeError("protected input exceeded its bound")
    return raw.decode("utf-8")

def atomic(path, value):
    path = Path(path)
    if path.exists() or path.is_symlink(): raise RuntimeError("evidence path is not fresh")
    temporary = path.with_name("." + path.name + ".tmp")
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    try:
        with os.fdopen(fd, "w") as output:
            json.dump(value, output, sort_keys=True, separators=(",", ":")); output.write("\n"); output.flush(); os.fsync(output.fileno())
        os.link(temporary, path)
    finally: temporary.unlink(missing_ok=True)

def validate_attestation(a):
    value=load(a.gate_attestation)
    required={"schema_version","run_id","kind","project","environment","context","cluster_uid","node_uids","kubeconfig","source_root","source_files","binary_sha256","expires_at"}
    if set(value)!=required or value["schema_version"]!=1 or not ID.fullmatch(str(value["run_id"])) or value["kind"]!="neon" or value["project"]!=a.project or value["environment"]!="development" or value["context"]!="k3d-hakopod-dev": raise RuntimeError("gate attestation identity is invalid")
    if Path(value["source_root"]).resolve()!=Path(a.source).resolve() or Path(value["kubeconfig"]).resolve()!=Path(a.kubeconfig).resolve() or not DIGEST.fullmatch(str(value["binary_sha256"])): raise RuntimeError("gate attestation paths or binary identity differ")
    files=value["source_files"]
    if not isinstance(files,dict) or not 10<=len(files)<=8192: raise RuntimeError("gate source inventory is incomplete")
    for relative,expected in files.items():
        path=Path(relative)
        if path.is_absolute() or path==Path(".") or ".." in path.parts or not DIGEST.fullmatch(str(expected)): raise RuntimeError("gate source identity is malformed")
        absolute=Path(a.source)/path
        if absolute.is_symlink() or not absolute.is_file() or hashlib.sha256(absolute.read_bytes()).hexdigest()!=expected: raise RuntimeError("gate source identity changed")
    nodes=value["node_uids"]
    if not isinstance(nodes,dict) or not 3<=len(nodes)<=8 or len(set(nodes.values()))!=len(nodes) or not value["cluster_uid"]: raise RuntimeError("gate cluster identity is incomplete")
    return value

def validate_contract(a):
    if sys.platform != "linux" or os.uname().machine not in ("x86_64", "amd64"): raise RuntimeError("native acceptance requires Linux AMD64")
    work = Path(a.work_dir)
    if not work.is_absolute() or work.exists() or work.is_symlink() or work.parent not in (Path("/tmp"), Path("/srv/hakopod-backup-scratch")) or not work.name.startswith("hakopod-neon-native-"): raise RuntimeError("work path is not fresh and bounded")
    api = urllib.parse.urlsplit(a.api_url)
    if api.scheme != "http" or api.hostname not in ("127.0.0.1", "localhost") or api.port is None or not 1 <= api.port <= 65535 or api.username is not None or api.password is not None or api.path not in ("", "/") or api.query or api.fragment: raise RuntimeError("API is not an exact loopback origin")
    validate_attestation(a)
    source, target, cancellation, images, identities = load(a.source_spec), load(a.target_spec), load(a.cancellation_target_spec), load(a.images), load(a.identities)
    for spec in (source, target, cancellation):
        neon = spec.get("neon", {})
        if spec.get("schema_version") != 1 or spec.get("kind") != "neon" or neon.get("safekeepers") != 3 or not 2 <= neon.get("pageservers", 0) <= 8 or not 1 <= neon.get("compute_replicas", 0) <= 6: raise RuntimeError("strict Neon topology is required")
    if len({spec.get("name") for spec in (source,target,cancellation)}) != 3 or len({spec["neon"].get("object_storage_prefix") for spec in (source,target,cancellation)}) != 3: raise RuntimeError("restore targets are not separate")
    if set(images) != COMPONENTS or any(not re.fullmatch(r"[^@]+@sha256:[0-9a-f]{64}", v) for v in images.values()): raise RuntimeError("image inventory is not exact and pinned")
    if set(identities) != COMPONENTS or any(set(v) != {"uid", "gid", "image"} or type(v["uid"]) is not int or type(v["gid"]) is not int or min(v["uid"], v["gid"]) <= 0 or v["image"] != images[name] for name, v in identities.items()): raise RuntimeError("identity inventory is incomplete")
    if not ID.fullmatch(a.destination_id) or a.destination_revision < 1: raise RuntimeError("destination identity is malformed")
    return source, target, cancellation

class Driver:
    def __init__(self, a):
        self.a=a; self.source_spec,self.target_spec,self.cancellation_target_spec=validate_contract(a); self.root=Path(a.work_dir); self.root.mkdir(mode=0o700); self.evidence=self.root/"evidence"; self.evidence.mkdir(mode=0o700)
        token=protected_text(a.token_file)
        if token != token.strip() or "\n" in token or "\r" in token: raise RuntimeError("API token is malformed")
        self.token=token; self.platforms=[]; self.bound={}; self.run_id=""; self.foreign=None; self.native={}; self.pending_revocation=None; self.pending_lifecycle=None; self.cleanup_started=False; self.cleanup_completed=False; self.deleted_platforms=set()
        if not 16 <= len(self.token) <= 4096: raise RuntimeError("API token is malformed")
        self.proxy_password_files={"source":a.source_proxy_password_file,"target":a.target_proxy_password_file,"cancellation":a.cancellation_proxy_password_file}; self.proxy_passwords={}
        values=[]
        for path in self.proxy_password_files.values():
            password=protected_text(path)
            if password!=password.strip() or any(char in password for char in "\r\n"): raise RuntimeError("proxy SQL credential is malformed")
            values.append(password)
        if len(set(values))!=3: raise RuntimeError("proxy SQL credentials must be distinct per platform")
        if not Path(a.psql).is_absolute() or not os.access(a.psql,os.X_OK): raise RuntimeError("PostgreSQL client executable is unavailable")
        if getattr(a,"scheduling_policy",""): self.command(self.bridge_argv("verify-scheduling"),120)
    def bridge_argv(self,action,*args):
        argv=[sys.executable,self.a.control_plane_bridge,action,"--kubeconfig",self.a.kubeconfig,*args]
        policy=getattr(self.a,"scheduling_policy","")
        if policy: argv.extend(["--scheduling-policy",policy,"--gate-attestation",self.a.gate_attestation])
        return argv
    def command(self, argv, timeout=30, data=None, env=None, expected=(0,), capture_stderr=False):
        if not isinstance(argv, (list, tuple)) or not 1 <= len(argv) <= 128 or any(not isinstance(item, str) or not item or "\x00" in item or len(item.encode()) > 8192 for item in argv): raise RuntimeError("acceptance command is malformed")
        if type(timeout) not in (int, float) or not 1 <= timeout <= 1800: raise RuntimeError("acceptance command timeout is invalid")
        if data is not None and (not isinstance(data, bytes) or len(data) > MAX_COMMAND_INPUT): raise RuntimeError("acceptance command input is invalid or unbounded")
        if not isinstance(expected,tuple) or not 1<=len(expected)<=8 or any(type(code) is not int or not 0<=code<=255 for code in expected): raise RuntimeError("acceptance command exit contract is invalid")
        def limits(): resource.setrlimit(resource.RLIMIT_FSIZE, (MAX_COMMAND_OUTPUT + 1, MAX_COMMAND_OUTPUT + 1))
        with tempfile.TemporaryFile() as stdout, tempfile.TemporaryFile() as stderr:
            process = subprocess.Popen(argv, stdin=subprocess.PIPE if data is not None else subprocess.DEVNULL, stdout=stdout, stderr=stderr, close_fds=True, preexec_fn=limits, env=env)
            try: process.communicate(data, timeout=timeout)
            except subprocess.TimeoutExpired:
                process.kill(); process.wait(timeout=5)
                raise RuntimeError("bounded acceptance command timed out")
            except BaseException:
                process.terminate()
                try:process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill(); process.wait(timeout=5)
                raise
            stdout.seek(0, os.SEEK_END); out_size=stdout.tell(); stderr.seek(0, os.SEEK_END); err_size=stderr.tell()
            if out_size > MAX_COMMAND_OUTPUT or err_size > MAX_COMMAND_OUTPUT or process.returncode not in expected: raise RuntimeError("bounded acceptance command failed; output withheld")
            (stderr if capture_stderr else stdout).seek(0); raw=(stderr if capture_stderr else stdout).read(MAX_COMMAND_OUTPUT + 1)
        try: return raw.decode("utf-8")
        except UnicodeDecodeError as error: raise RuntimeError("acceptance command output is not UTF-8") from error
    def k(self,*argv,timeout=30): return self.command(["kubectl","--request-timeout=15s","--kubeconfig",self.a.kubeconfig,"--context","k3d-hakopod-dev",*argv],timeout)
    def api(self,method,path,body=None,idem=None,expected=(200,202)):
        parsed = urllib.parse.urlsplit(path)
        if method not in ("GET", "POST") or not path.startswith("/api/v1/") or parsed.scheme or parsed.netloc or parsed.query or parsed.fragment or ".." in parsed.path.split("/") or len(path) > 2048: raise RuntimeError("API request path is invalid")
        raw=None if body is None else json.dumps(body,separators=(",",":")).encode(); headers={"authorization":"Bearer "+self.token,"accept":"application/json"}
        if raw is not None and len(raw) > 1 << 20: raise RuntimeError("API request body exceeded its bound")
        if raw is not None: headers["content-type"]="application/json"
        if idem:
            if not isinstance(idem, str) or not 8 <= len(idem) <= 128 or any(ord(char) < 33 or ord(char) > 126 for char in idem): raise RuntimeError("idempotency key is invalid")
            headers["idempotency-key"]=idem
        req=urllib.request.Request(self.a.api_url.rstrip("/")+path,data=raw,headers=headers,method=method)
        try:
            with HTTP.open(req,timeout=105 if path.endswith("/native-migrate") else 45 if path.endswith("/native-probe") else 15) as response:
                if response.geturl() != req.full_url: raise RuntimeError("API redirect was refused")
                data,status=response.read((1<<20)+1),response.status
        except urllib.error.HTTPError as error: data,status=error.read((1<<20)+1),error.code
        if len(data)>1<<20 or status not in expected: raise RuntimeError("API returned an unexpected bounded response")
        return json.loads(data) if data else {}
    def wait(self,path,seconds=900,allowed=("succeeded",),observe=None):
        deadline=time.monotonic()+seconds
        while time.monotonic()<deadline:
            item=self.api("GET",path)
            if observe is not None: observe()
            if item.get("status") in {"succeeded","failed","cancelled"}:
                if item["status"] not in allowed: raise RuntimeError("operation terminal state was not accepted")
                return item
            time.sleep(2)
        raise RuntimeError("operation exceeded its time bound")
    def wait_phase(self,path,phases,seconds=300):
        deadline=time.monotonic()+seconds
        while time.monotonic()<deadline:
            item=self.api("GET",path)
            if item.get("phase") in phases: return item
            if item.get("status") in {"succeeded","failed","cancelled"}: raise RuntimeError("operation became terminal before the required phase")
            time.sleep(0.1)
        raise RuntimeError("operation did not reach the required phase")
    def create(self,spec):
        body={"project":self.a.project,"environment":"development","expected_revision":0,"kind":"create","spec":spec}; reviewed=self.api("POST","/api/v1/managed-platforms/reviews",body)
        if reviewed.get("blocked") is not False or not reviewed.get("review",{}).get("id"): raise RuntimeError("native capability gate remains closed")
        platform=reviewed["platform"]
        if platform.get("project")!=self.a.project or platform.get("environment")!="development" or platform.get("spec")!=spec or reviewed["review"].get("expected_revision")!=0: raise RuntimeError("create review did not echo the exact request")
        self.command(self.bridge_argv("allow","--platform-id",platform["id"]),240)
        request={"id":platform["id"],"project":platform["project"],"environment":platform["environment"],"expected_revision":0,"kind":"create","spec":platform["spec"],"review":reviewed["review"]}
        op=self.api("POST","/api/v1/managed-platforms/operations",request,"neon-create-"+platform["id"])
        if not ID.fullmatch(str(platform.get("id"))) or not ID.fullmatch(str(op.get("id"))) or op.get("platform_id")!=platform["id"] or op.get("kind")!="create": raise RuntimeError("accepted create operation identity is invalid")
        self.platforms.append((platform["id"],spec["name"],op["id"]))
        observe=lambda:self.claim_namespace(platform["id"],op["id"])
        observe(); completed=self.wait("/api/v1/managed-platform-operations/"+op["id"],observe=observe)
        if not observe(): raise RuntimeError("completed create operation has no owned namespace")
        return platform,completed
    def delete(self,platform_id,name,seconds=900):
        current=self.api("GET","/api/v1/managed-platforms/"+platform_id)
        if current.get("id")!=platform_id or current.get("project")!=self.a.project or current.get("environment")!="development" or current.get("spec",{}).get("name")!=name: raise RuntimeError("cleanup API resource identity changed")
        body={"id":platform_id,"project":self.a.project,"environment":"development","expected_revision":current["revision"],"kind":"delete","confirm_name":name,"spec":current["spec"]}
        reviewed=self.api("POST","/api/v1/managed-platforms/reviews",body); body["review"]=reviewed["review"]; op=self.api("POST","/api/v1/managed-platforms/operations",body,"neon-delete-"+platform_id); return self.wait("/api/v1/managed-platform-operations/"+op["id"],seconds)
    def recover(self,intent,label):
        review=self.api("POST","/api/v1/managed-platform-recovery/reviews",intent); op=self.api("POST","/api/v1/managed-platform-recovery/operations",dict(intent,review=review),"neon-"+label+"-"+self.run_id); return self.wait("/api/v1/managed-platform-recovery-operations/"+op["id"],1200)
    def namespace(self,pid): return json.loads(self.k("get","namespace","managed-platform-"+pid,"-o","json"))
    def claim_namespace(self,pid,opid):
        raw=self.k("get","namespace","managed-platform-"+pid,"--ignore-not-found","-o","json")
        if not raw.strip(): return False
        metadata=json.loads(raw).get("metadata",{}); labels=metadata.get("labels",{})
        if metadata.get("name")!="managed-platform-"+pid or not metadata.get("uid") or labels.get("hakopod.io/owner-operation-id")!=opid: raise RuntimeError("created namespace ownership differs from accepted operation")
        identity=(metadata["uid"],opid); existing=self.bound.get(pid)
        if existing is not None and existing!=identity: raise RuntimeError("created namespace identity changed")
        self.bound[pid]=identity; return True
    def pods(self,pid):
        ns="managed-platform-"+pid; pods=json.loads(self.k("-n",ns,"get","pods","-l","hakopod.io/managed-platform-id="+pid,"-o","json"))["items"]
        if not pods or len(pods)>32: raise RuntimeError("pod inventory is incomplete")
        return ns,pods
    def record(self,case,value):
        path=self.evidence/(case+".json"); atomic(path,value); self.command([sys.executable,str(Path(__file__).with_name("evidence.py")),"record","--state",str(self.evidence/"state.json"),"--events",str(self.evidence/"events.json"),"--case",case,"--observation",str(path)])
    def ownership(self,pid,opid):
        ns="managed-platform-"+pid; namespace=self.namespace(pid); labels=namespace["metadata"].get("labels",{})
        if labels.get("hakopod.io/owner-operation-id")!=opid: raise RuntimeError("namespace ownership differs from operation")
        raw=self.k("-n",ns,"get","all,configmap,secret,pvc,networkpolicy","-o","jsonpath={range .items[*]}{.metadata.labels.hakopod\\.io/resource-intent-id}{'\\n'}{end}")
        intents=sorted({x for x in raw.splitlines() if ID.fullmatch(x)})
        if not 3<=len(intents)<=16: raise RuntimeError("resource intent inventory is incomplete")
        sentinel="hakopod-neon-sentinel-"+self.run_id[:12]
        manifest=json.dumps({"apiVersion":"v1","kind":"Namespace","metadata":{"name":sentinel,"labels":{"hakopod.io/native-acceptance-run":self.run_id}}}).encode(); self.command(["kubectl","--kubeconfig",self.a.kubeconfig,"--context","k3d-hakopod-dev","create","-f","-"],data=manifest)
        sentinel_uid=self.namespace_uid(sentinel); name="foreign-neon-"+self.run_id[:12]; manifest=json.dumps({"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":name,"namespace":sentinel},"data":{"owner":"foreign"}}).encode(); self.command(["kubectl","--kubeconfig",self.a.kubeconfig,"--context","k3d-hakopod-dev","create","-f","-"],data=manifest); self.foreign=(sentinel,sentinel_uid,name); time.sleep(3)
        if json.loads(self.k("-n",sentinel,"get","configmap",name,"-o","json")).get("data")!={"owner":"foreign"}: raise RuntimeError("foreign resource changed")
        current=self.api("GET","/api/v1/managed-platforms/"+pid)
        probe=self.api("POST","/api/v1/managed-platforms/"+pid+"/native-probe",{"expected_revision":current["revision"]})
        if probe.get("platform_id")!=pid or probe.get("platform_revision")!=current["revision"] or probe.get("namespace_uid")!=namespace["metadata"]["uid"] or probe.get("ownership_capability_verified") is not True: raise RuntimeError("native provider identity probe was not verified")
        self.native[pid]=probe
        if any(probe.get(field) is not True for field in ("wrong_owner_refused","wrong_deletion_token_refused")): raise RuntimeError("provider ownership rejection was not observed")
        return {"run_id":self.run_id,"platform_id":pid,"namespace_uid":namespace["metadata"]["uid"],"mutation_capability_required":probe["ownership_capability_verified"],"foreign_owner_refused":probe["wrong_owner_refused"],"deletion_token_required":probe["wrong_deletion_token_refused"],"owner_operation_id":opid,"resource_intent_ids":intents}
    def runtime(self,pid,uid):
        ns,pods=self.pods(pid); roles={p["metadata"]["labels"].get("hakopod.io/neon-role") for p in pods}; required={"storage-controller","pageserver","safekeeper","compute","proxy"}
        if not required<=roles: raise RuntimeError("runtime role inventory is incomplete")
        services=json.loads(self.k("-n",ns,"get","services","-o","json"))["items"]
        service_names={s.get("metadata",{}).get("name") for s in services}
        expected_services={"neon-storage-controller","neon-proxy"}|{"neon-pageserver-"+str(i) for i in range(self.source_spec["neon"]["pageservers"])}|{"neon-safekeeper-"+str(i) for i in range(3)}|{"neon-compute-"+str(i) for i in range(self.source_spec["neon"]["compute_replicas"])}|{"neon-compute-"+str(i)+"-control" for i in range(self.source_spec["neon"]["compute_replicas"])}
        if not expected_services<=service_names: raise RuntimeError("service inventory is incomplete")
        compute=next(p for p in pods if p["metadata"]["labels"].get("hakopod.io/neon-role")=="compute"); containers={c["name"] for c in compute["spec"]["containers"]}
        if not {"compute","compute-tls"}<=containers: raise RuntimeError("compute TLS sidecar is absent")
        probe=self.native.get(pid,{}); computes=probe.get("compute_names"); tls_observed=probe.get("tls",{})
        expected_computes=["compute-"+str(i) for i in range(self.source_spec["neon"]["compute_replicas"])]
        tls_services=required|{"broker","controller-database","compute-sql"}
        if any(tls_observed.get(field) is not True for field in ("client_verification_enforced","server_verified","plaintext_refused")) or set(tls_observed.get("services",[]))!=tls_services or tls_observed.get("compute_sql_names")!=expected_computes: raise RuntimeError("provider and compute SQL TLS evidence is missing")
        if not ID.fullmatch(str(probe.get("tenant_id"))) or not ID.fullmatch(str(probe.get("timeline_id"))) or type(probe.get("tenant_generation")) is not int or type(probe.get("timeline_generation")) is not int or sorted(computes or [])!=expected_computes or any(probe.get(field) is not True for field in ("tenant_created","timeline_created","compute_started")): raise RuntimeError("native durable lifecycle identity is missing")
        tls={"run_id":self.run_id,"platform_id":pid,"namespace_uid":uid,"client_verification_enforced":tls_observed["client_verification_enforced"],"server_verified":tls_observed["server_verified"],"plaintext_refused":tls_observed["plaintext_refused"],"services":sorted(tls_observed["services"]),"compute_sql_names":tls_observed["compute_sql_names"]}
        lifecycle={"run_id":self.run_id,"platform_id":pid,"namespace_uid":uid,"tenant_id":probe["tenant_id"],"timeline_id":probe["timeline_id"],"tenant_generation":probe["tenant_generation"],"timeline_generation":probe["timeline_generation"],"compute_names":computes,"created":True,"stopped":False,"deleted":False,"branch_created":probe["timeline_created"]}
        return tls,lifecycle
    def managed_tls(self,pid,revision,operation_id):
        namespace="managed-platform-"+pid; ca=self.root/"managed-tls-ca.crt"; injected_path=self.evidence/"managed-tls-injected.json"
        common=[sys.executable,self.a.managed_tls_helper,"--kubectl","kubectl","--kubeconfig",self.a.kubeconfig,"--namespace",namespace,"--platform-id",pid,"--platform-revision",str(revision),"--owner-operation-id",operation_id,"--proxy-host","neon-proxy","--api-url",self.a.api_url.rstrip("/"),"--api-token-file",self.a.token_file,"--project",self.a.project,"--environment","development","--ca-file",str(ca),"--private-tmp",str(self.root),"--spec-digest-helper",self.a.runtime_spec_digest_helper,"--psql-command-file",self.a.control_psql_command_file,"--openssl",self.a.openssl,"--local-port",str(self.a.local_port)]
        with self.proxy_forward(pid,lifetime=900):
            before=json.loads(self.command([*common,"observe"],60))
            injected=json.loads(self.command([*common,"inject"],180))
            atomic(injected_path,injected)
            renewed=json.loads(self.command([*common,"await-renewal","--input",str(injected_path)],480))
        return {"before":before,"injection":injected,"renewal":renewed}
    def proxy_forward(self,pid,local_port=None,probe=False,maximum=16,lifetime=180):
        if pid not in self.bound: raise RuntimeError("proxy stream platform is not bound")
        spec=importlib.util.spec_from_file_location("neon_proxy_stream",Path(__file__).with_name("proxy-stream.py"))
        module=importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
        namespace_uid,operation_id=self.bound[pid]
        target=module.ProxyTarget(self.a.kubeconfig,pid,namespace_uid,operation_id,load(self.a.images)["proxy"],lambda *args:json.loads(self.k(*args)),probe=probe)
        target()
        return module.STREAM.OwnedPodForward(self.a.local_port if local_port is None else local_port,target,maximum,lifetime=lifetime)
    def proxy_auth_message(self,namespace,target,local_port,ca,platform_id):
        if namespace!="managed-platform-"+platform_id or target!="deployment/hakopod-neon-wrong-ca": raise RuntimeError("issuer probe target is invalid")
        with self.proxy_forward(platform_id,local_port=local_port,probe=True):
            raw=socket.create_connection(("127.0.0.1",local_port),timeout=10)
            try:
                raw.sendall(struct.pack("!II",8,80877103))
                if raw.recv(1)!=b"S": raise RuntimeError("Neon proxy trust probe refused TLS")
                context=ssl.create_default_context(cafile=str(ca))
                with context.wrap_socket(raw,server_hostname="neon-proxy") as connection:
                    payload=struct.pack("!I",196608)+b"user\x00cloud_admin\x00database\x00postgres\x00options\x00endpoint="+platform_id.encode()+b"\x00\x00"
                    connection.sendall(struct.pack("!I",len(payload)+4)+payload)
                    connection.settimeout(10)
                    return connection.recv(1)
            finally: raw.close()
    def control_plane_trust(self,pid):
        namespace="managed-platform-"+pid; deployment=json.loads(self.k("-n",namespace,"get","deployment","neon-proxy","-o","json"))
        secrets=[volume.get("secret",{}).get("secretName") for volume in deployment["spec"]["template"]["spec"]["volumes"] if volume.get("name")=="proxy-auth"]
        if len(secrets)!=1 or not re.fullmatch(r"platform-tls-proxy-[0-9a-f]{16}-r1",secrets[0] or ""): raise RuntimeError("active Neon proxy TLS snapshot is ambiguous")
        base=["--platform-id",pid]
        self.command(self.bridge_argv("probe-start",*base,"--proxy-secret",secrets[0],"--ca-kind","correct"),240)
        try: correct=self.proxy_auth_message(namespace,"deployment/hakopod-neon-wrong-ca",self.a.local_port+1,self.root/"managed-tls-ca.crt",pid)
        finally:self.command(self.bridge_argv("probe-stop",*base),240)
        if correct!=b"R": raise RuntimeError("correct control-plane issuer did not reach PostgreSQL authentication")
        self.command(self.bridge_argv("probe-start",*base,"--proxy-secret",secrets[0],"--ca-kind","wrong"),240)
        try:
            wrong=self.proxy_auth_message(namespace,"deployment/hakopod-neon-wrong-ca",self.a.local_port+1,self.root/"managed-tls-ca.crt",pid)
            self.command(self.bridge_argv("probe-verify-wrong",*base),60)
        finally:self.command(self.bridge_argv("probe-stop",*base),240)
        if wrong!=b"E": raise RuntimeError("wrong control-plane issuer was not rejected")
        self.authenticated_proxy_query(pid)
        return {"correct_issuer_reached_authentication":True,"wrong_issuer_refused":True,"proxy_authenticated_query":True}
    def platform_ca(self,pid):
        if not isinstance(pid,str) or not ID.fullmatch(pid) or pid not in self.bound:
            raise RuntimeError("proxy trust platform identity is not bound")
        cache=getattr(self,"proxy_ca_files",{})
        if pid in cache:
            path,digest=cache[pid]
            if hashlib.sha256(protected_text(path,64<<10).encode()).hexdigest()!=digest:
                raise RuntimeError("cached platform trust changed")
            return path
        if len(cache)>=3: raise RuntimeError("platform trust inventory exceeded its bound")
        trust=self.api("GET","/api/v1/managed-platforms/"+pid+"/trust")
        if not isinstance(trust,dict) or set(trust)!={"certificate_pem","fingerprint","issuer","not_before","expires_at"}:
            raise RuntimeError("platform trust response is incomplete")
        pem=trust["certificate_pem"]
        if not isinstance(pem,str) or not 1<len(pem.encode())<=64<<10 or re.fullmatch(r"-----BEGIN CERTIFICATE-----\n[A-Za-z0-9+/=\r\n]+\n-----END CERTIFICATE-----\n?",pem) is None:
            raise RuntimeError("platform trust must contain one public CA")
        try:
            der=ssl.PEM_cert_to_DER_cert(pem)
            context=ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
            context.load_verify_locations(cadata=pem)
            fingerprint=hashlib.sha256(der).hexdigest()
            issuer=["CN=Hakopod platform "+pid,"OU=managed-platform-"+pid]
            if context.cert_store_stats()["x509_ca"]!=1 or trust["fingerprint"]!=fingerprint or sorted(trust["issuer"].split(","))!=sorted(issuer):
                raise ValueError("trust identity differs")
            from datetime import datetime, timezone
            before=datetime.fromisoformat(trust["not_before"].replace("Z","+00:00"))
            expires=datetime.fromisoformat(trust["expires_at"].replace("Z","+00:00"))
            if before.tzinfo is None or expires.tzinfo is None or not before<=datetime.now(timezone.utc)<expires:
                raise ValueError("trust validity differs")
        except (ValueError,TypeError,AttributeError,ssl.SSLError):
            raise RuntimeError("platform CA identity or validity is invalid") from None
        path=self.root/("proxy-ca-"+pid+"-"+fingerprint[:16]+".crt")
        fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
        with os.fdopen(fd,"w") as output:
            output.write(pem); output.flush(); os.fsync(output.fileno())
        cache[pid]=(path,hashlib.sha256(pem.encode()).hexdigest())
        self.proxy_ca_files=cache
        return path
    def proxy_environment(self,pid,password):
        if not ID.fullmatch(pid): raise RuntimeError("proxy SQL endpoint identity is invalid")
        if password!=password.strip() or any(char in password for char in "\r\n"): raise RuntimeError("proxy SQL credential is malformed")
        environment={key:value for key,value in os.environ.items() if not key.startswith("PG") and key!="PSQLRC"}
        ca=self.platform_ca(pid)
        environment.update(PGPASSWORD=password,PGOPTIONS="endpoint="+pid,PGAPPNAME="hakopod_neon_native_acceptance",PGCONNECT_TIMEOUT="10",PGHOST="neon-proxy",PGHOSTADDR="127.0.0.1",PGPORT=str(self.a.local_port),PGUSER="cloud_admin",PGDATABASE="postgres",PGSSLMODE="verify-full",PGSSLROOTCERT=str(ca))
        return environment
    def proxy_query(self,pid,password,statement,expect_success=True,timeout=45,expected_error=None):
        if not isinstance(statement,str) or not statement or len(statement)>8192 or "\x00" in statement: raise RuntimeError("proxy SQL statement is invalid")
        if not expect_success and (not isinstance(expected_error,str) or not expected_error): raise RuntimeError("proxy SQL failure contract is missing")
        environment=self.proxy_environment(pid,password)
        with self.proxy_forward(pid,lifetime=max(180,timeout+30)):
            observed=self.command([self.a.psql,"-XAtw","-v","ON_ERROR_STOP=1","-c",statement],timeout,env=environment,expected=(0,) if expect_success else (1,2,3),capture_stderr=not expect_success).strip()
            if not expect_success and re.search(expected_error,observed,re.IGNORECASE) is None: raise RuntimeError("Neon proxy SQL failed for an unexpected reason")
            return observed
    def authenticated_proxy_query(self,pid):
        password=self.proxy_password(pid)
        observed=self.proxy_query(pid,password,"SELECT current_user || ':' || current_database()")
        if observed!="cloud_admin:postgres": raise RuntimeError("Neon proxy SQL authenticated identity differs")
    def proxy_password(self,pid):
        path=self.proxy_passwords.get(pid)
        if path is None: raise RuntimeError("proxy SQL credential is not bound to the platform")
        return protected_text(path)
    def isolation_and_credentials(self,source,target,marker):
        password=self.proxy_password(source); target_password=self.proxy_password(target)
        source_probe=self.native[source]
        target_current=self.api("GET","/api/v1/managed-platforms/"+target)
        target_probe=self.api("POST","/api/v1/managed-platforms/"+target+"/native-probe",{"expected_revision":target_current["revision"]})
        if target_probe.get("platform_id")!=target or target_probe.get("platform_revision")!=target_current["revision"] or not ID.fullmatch(str(target_probe.get("tenant_id"))) or not ID.fullmatch(str(target_probe.get("timeline_id"))): raise RuntimeError("isolation target identity was not verified")
        if target_probe["tenant_id"]==source_probe.get("tenant_id") or target_probe["timeline_id"]==source_probe.get("timeline_id"): raise RuntimeError("separate platforms shared a Neon tenant or timeline")
        source_value=self.proxy_query(source,password,"SELECT v FROM hakopod_native_acceptance WHERE k='backup'")
        target_empty=self.proxy_query(target,target_password,"SELECT to_regclass('public.hakopod_native_acceptance') IS NULL")
        if source_value!=marker or target_empty!="t": raise RuntimeError("Neon tenant SQL isolation was not observed")
        auth_error=r"password authentication failed|authentication failed"
        self.proxy_query(source,password+"-invalid","SELECT 1",expect_success=False,timeout=20,expected_error=auth_error)
        self.proxy_query(target,password,"SELECT 1",expect_success=False,timeout=20,expected_error=auth_error); self.proxy_query(source,target_password,"SELECT 1",expect_success=False,timeout=20,expected_error=auth_error)
        return {"run_id":self.run_id,"platform_id":source,"namespace_uid":self.bound[source][0],"target_platform_id":target,"target_namespace_uid":self.bound[target][0],"source_tenant_id":source_probe["tenant_id"],"source_timeline_id":source_probe["timeline_id"],"target_tenant_id":target_probe["tenant_id"],"target_timeline_id":target_probe["timeline_id"],"source_marker_verified":True,"target_marker_absent":True,"bad_credentials_refused":True,"cross_platform_credentials_refused":True}
    def connection_limits(self,pid):
        password=self.proxy_password(pid); maximum=int(self.proxy_query(pid,password,"SHOW max_connections"))
        if not 10<=maximum<=120: raise RuntimeError("Neon connection limit is absent or outside the bounded probe")
        environment=self.proxy_environment(pid,password); outputs=[]; processes=[]; observer=None
        application="hakopod_limit_"+self.run_id; environment["PGAPPNAME"]=application
        observer_output=tempfile.TemporaryFile()
        forward=self.proxy_forward(pid,maximum=maximum+10).start()
        try:
            observer_environment=dict(environment,PGAPPNAME="hakopod_limit_observer_"+self.run_id)
            observer=subprocess.Popen([self.a.psql,"-XAtqw","-v","ON_ERROR_STOP=1"],stdin=subprocess.PIPE,stdout=observer_output,stderr=observer_output,close_fds=True,env=observer_environment,preexec_fn=process_output_limit)
            capacity_query="SELECT 'hakopod-capacity:' || json_build_object('max_connections',current_setting('max_connections')::int,'superuser_reserved_connections',current_setting('superuser_reserved_connections')::int,'reserved_connections',current_setting('reserved_connections')::int,'role_superuser',(SELECT rolsuper FROM pg_roles WHERE rolname=current_user),'role_reserved',pg_has_role(current_user,'pg_use_reserved_connections','member'),'baseline_pids',(SELECT COALESCE(json_agg(pid ORDER BY pid),'[]'::json) FROM pg_stat_activity WHERE backend_type='client backend'))::text; SELECT 'hakopod-observer-ready';\n"
            observer.stdin.write(capacity_query.encode()); observer.stdin.flush()
            deadline=time.monotonic()+15
            while time.monotonic()<deadline:
                if observer.poll() is not None: raise RuntimeError("Neon connection observer exited")
                if "hakopod-observer-ready" in bounded_process_output(observer_output).splitlines(): break
                time.sleep(.1)
            else: raise RuntimeError("Neon connection observer did not authenticate and flush its marker")
            capacity_lines=[line.removeprefix("hakopod-capacity:") for line in bounded_process_output(observer_output).splitlines() if line.startswith("hakopod-capacity:")]
            if len(capacity_lines)!=1: raise RuntimeError("Neon connection capacity observation is missing")
            capacity=json.loads(capacity_lines[0]); baseline=capacity.get("baseline_pids",[])
            if capacity.get("max_connections")!=maximum or any(type(capacity.get(key)) is not int or not 0<=capacity[key]<maximum for key in ("superuser_reserved_connections","reserved_connections")) or any(type(capacity.get(key)) is not bool for key in ("role_superuser","role_reserved")) or not isinstance(baseline,list) or not 1<=len(baseline)<=8 or any(type(pid) is not int or pid<1 for pid in baseline) or len(set(baseline))!=len(baseline): raise RuntimeError("Neon connection capacity observation is invalid")
            effective=maximum if capacity["role_superuser"] else maximum-capacity["superuser_reserved_connections"]-(0 if capacity["role_reserved"] else capacity["reserved_connections"])
            expected_held=effective-len(baseline)
            if expected_held<1: raise RuntimeError("Neon connection baseline leaves no bounded saturation capacity")
            for _ in range(maximum+8):
                output=tempfile.TemporaryFile(); outputs.append(output)
                processes.append(subprocess.Popen([self.a.psql,"-XAtw","-v","ON_ERROR_STOP=1","-c","SELECT 'hakopod-held:' || pg_backend_pid()","-c","SELECT pg_sleep(45)"],stdin=subprocess.DEVNULL,stdout=output,stderr=output,close_fds=True,env=environment,preexec_fn=process_output_limit))
            deadline=time.monotonic()+20; held_pids=set(); refused=0
            while time.monotonic()<deadline:
                held_pids=set(); refused=0
                for process,output in zip(processes,outputs):
                    observed=bounded_process_output(output); code=process.poll()
                    marker=re.findall(r"^hakopod-held:([1-9][0-9]*)$",observed,re.MULTILINE)
                    if code is None and len(marker)==1: held_pids.add(int(marker[0]))
                    elif code is not None:
                        if code==0 or not re.search(r"\bFATAL:\s*(?:sorry,\s*)?(?:too many clients already|too many connections|remaining connection slots are reserved[^\r\n]*)\s*$",observed,re.IGNORECASE|re.MULTILINE): raise RuntimeError("Neon excess client failed for an unexpected reason")
                        refused+=1
                if len(held_pids)+refused==maximum+8: break
                time.sleep(.1)
            else: raise RuntimeError("Neon connection clients did not authenticate or return a classified refusal")
            held=len(held_pids)
            if held!=expected_held or refused<1: raise RuntimeError("Neon connection limit differs from its exact role and baseline capacity")
            deadline=time.monotonic()+5; backend_pids=set()
            for attempt in range(25):
                if time.monotonic()>=deadline: break
                prefix="hakopod-active-"+str(attempt)+":"; complete="hakopod-observer-complete-"+str(attempt)
                baseline_prefix="hakopod-baseline-"+str(attempt)+":"
                query="SELECT '"+prefix+"' || pid FROM pg_stat_activity WHERE application_name='"+application+"' AND state='active' AND wait_event='PgSleep' ORDER BY pid; SELECT '"+baseline_prefix+"' || COALESCE(json_agg(pid ORDER BY pid),'[]'::json)::text FROM pg_stat_activity WHERE backend_type='client backend' AND application_name!='"+application+"'; SELECT '"+complete+"';\n"
                observer.stdin.write(query.encode()); observer.stdin.flush()
                while time.monotonic()<deadline:
                    observed=bounded_process_output(observer_output)
                    if complete in observed.splitlines(): break
                    if observer.poll() is not None: raise RuntimeError("Neon connection observer exited during saturation")
                    time.sleep(.05)
                else: raise RuntimeError("Neon connection observer did not flush its live backend observation")
                backend_pids={int(value) for value in re.findall(r"^"+prefix+r"([1-9][0-9]*)$",observed,re.MULTILINE)}
                baseline_lines=[line.removeprefix(baseline_prefix) for line in observed.splitlines() if line.startswith(baseline_prefix)]
                if len(baseline_lines)!=1 or json.loads(baseline_lines[0])!=baseline: raise RuntimeError("Neon connection baseline changed during saturation")
                if backend_pids==held_pids: break
                time.sleep(.1)
            if backend_pids!=held_pids or sum(process.poll() is None for process in processes)!=held: raise RuntimeError("Neon held clients were not independently observed sleeping in PostgreSQL")
        finally:
            if observer is not None: processes.append(observer)
            for process in processes:
                if process.poll() is None: process.terminate()
            for process in processes:
                try:process.wait(timeout=5)
                except subprocess.TimeoutExpired: process.kill(); process.wait(timeout=5)
            try:
                for output in outputs: output.close()
            finally:
                try: forward.close()
                finally:
                    observer_output.close()
                    forward.check_healthy()
        if self.proxy_query(pid,password,"SELECT 1")!="1": raise RuntimeError("Neon SQL did not recover after the connection-limit probe")
        return {"run_id":self.run_id,"platform_id":pid,"namespace_uid":self.bound[pid][0],"configured_max_connections":maximum,"attempted_connections":maximum+8,"concurrent_connections":held,"refused_connections":refused,"backend_pids":sorted(backend_pids),"baseline_backend_pids":baseline,"superuser_reserved_connections":capacity["superuser_reserved_connections"],"reserved_connections":capacity["reserved_connections"],"role_superuser":capacity["role_superuser"],"role_reserved":capacity["role_reserved"],"effective_connection_limit":effective,"held_markers_flushed":True,"sleeping_backends_verified":True,"baseline_unchanged":True,"service_recovered":True}
    def absent_safekeepers(self,namespace,stopped,old_uids):
        sets=json.loads(self.k("-n",namespace,"get","statefulsets",*stopped,"-o","json"))["items"]
        if len(sets)!=2 or {item["metadata"]["name"]:item["metadata"]["uid"] for item in sets}!=stopped: raise RuntimeError("Neon WAL quorum StatefulSet ownership changed")
        if any(item.get("spec",{}).get("replicas")!=0 for item in sets): return False
        pods=json.loads(self.k("-n",namespace,"get","pods","-o","json"))["items"]
        if len(pods)>32: raise RuntimeError("Neon WAL quorum pod inventory exceeded its bound")
        for pod in pods:
            metadata=pod["metadata"]
            if metadata["uid"] in old_uids or any(metadata.get("name","").startswith(name+"-") for name in stopped) or any(ref.get("kind")=="StatefulSet" and (ref.get("name") in stopped or ref.get("uid") in stopped.values()) for ref in metadata.get("ownerReferences",[])): return False
        return True
    def wal_quorum_fencing(self,pid):
        namespace="managed-platform-"+pid; password=self.proxy_password(pid)
        sets=json.loads(self.k("-n",namespace,"get","statefulsets","-l","hakopod.io/managed-platform-id="+pid,"-o","json"))["items"]
        safekeeper_sets=sorted((item for item in sets if item["metadata"].get("labels",{}).get("hakopod.io/neon-role")=="safekeeper"),key=lambda item:item["metadata"]["name"])
        if len(safekeeper_sets)!=3 or [item["metadata"]["name"] for item in safekeeper_sets]!=["neon-safekeeper-"+str(i) for i in range(3)] or any(item.get("spec",{}).get("replicas")!=1 for item in safekeeper_sets): raise RuntimeError("Neon WAL quorum subject inventory is not exact")
        stopped={item["metadata"]["name"]:item["metadata"]["uid"] for item in safekeeper_sets[1:]}
        pods=json.loads(self.k("-n",namespace,"get","pods","-l","hakopod.io/neon-role=safekeeper","-o","json"))["items"]
        stopped_uids={pod["metadata"]["uid"] for pod in pods if any(ref.get("kind")=="StatefulSet" and ref.get("name") in stopped and ref.get("uid")==next(item["metadata"]["uid"] for item in safekeeper_sets if item["metadata"]["name"]==ref.get("name")) for ref in pod["metadata"].get("ownerReferences",[]))}
        if len(stopped_uids)!=2: raise RuntimeError("Neon WAL quorum pod ownership is not exact")
        try:
            for name in stopped:self.k("-n",namespace,"scale","statefulset/"+name,"--current-replicas=1","--replicas=0",timeout=60)
            deadline=time.monotonic()+120
            while time.monotonic()<deadline:
                if self.absent_safekeepers(namespace,stopped,stopped_uids): break
                time.sleep(2)
            else: raise RuntimeError("Neon WAL quorum loss did not become observable")
            if not self.absent_safekeepers(namespace,stopped,stopped_uids): raise RuntimeError("Neon WAL quorum loss changed before the write")
            self.proxy_query(pid,password,"SET statement_timeout='8s'; INSERT INTO hakopod_native_acceptance VALUES ('wal-quorum','must-not-commit') ON CONFLICT (k) DO UPDATE SET v=excluded.v",expect_success=False,timeout=20,expected_error=r"canceling statement due to statement timeout|could not.*safekeeper|quorum")
            if not self.absent_safekeepers(namespace,stopped,stopped_uids): raise RuntimeError("Neon WAL quorum loss changed during the write")
        finally:
            restore_errors=[]
            for name in stopped:
                try:self.k("-n",namespace,"scale","statefulset/"+name,"--current-replicas=0","--replicas=1",timeout=60)
                except Exception:restore_errors.append(name)
            for name in stopped:
                try:self.k("-n",namespace,"rollout","status","statefulset/"+name,"--timeout=180s",timeout=200)
                except Exception:restore_errors.append(name)
        if restore_errors: raise RuntimeError("Neon WAL quorum restoration failed")
        if self.proxy_query(pid,password,"SELECT count(*) FROM hakopod_native_acceptance WHERE k='wal-quorum'")!="0": raise RuntimeError("Neon WAL-fenced write committed despite its refusal")
        recovered=self.proxy_query(pid,password,"INSERT INTO hakopod_native_acceptance VALUES ('wal-recovered','yes') ON CONFLICT (k) DO UPDATE SET v=excluded.v; SELECT v FROM hakopod_native_acceptance WHERE k='wal-recovered'")
        if recovered!="INSERT 0 1\nyes" and not recovered.endswith("\nyes"): raise RuntimeError("Neon WAL quorum did not recover")
        return {"run_id":self.run_id,"platform_id":pid,"namespace_uid":self.bound[pid][0],"safekeeper_count":3,"stopped_safekeepers":2,"stopped_statefulsets":stopped,"stopped_pod_uids":sorted(stopped_uids),"replicas_zero_before_write":True,"replicas_zero_after_refusal":True,"replacement_pods_absent":True,"write_refused_without_quorum":True,"fenced_write_absent":True,"quorum_restored":True,"write_recovered":True}
    def controller_recovery(self,pid):
        namespace="managed-platform-"+pid; before=self.api("POST","/api/v1/managed-platforms/"+pid+"/native-probe",{"expected_revision":self.api("GET","/api/v1/managed-platforms/"+pid)["revision"]})
        sets=json.loads(self.k("-n",namespace,"get","statefulsets","-l","hakopod.io/neon-role=storage-controller","-o","json"))["items"]
        pods=json.loads(self.k("-n",namespace,"get","pods","-l","hakopod.io/neon-role=storage-controller","-o","json"))["items"]
        if len(sets)!=1 or len(pods)!=1 or not any(ref.get("kind")=="StatefulSet" and ref.get("name")==sets[0]["metadata"]["name"] and ref.get("uid")==sets[0]["metadata"]["uid"] for ref in pods[0]["metadata"].get("ownerReferences",[])): raise RuntimeError("Neon controller recovery subject is not exact")
        old_uid=pods[0]["metadata"]["uid"]; delete=json.dumps({"apiVersion":"v1","kind":"DeleteOptions","preconditions":{"uid":old_uid}}).encode(); self.command(["kubectl","--request-timeout=15s","--kubeconfig",self.a.kubeconfig,"--context","k3d-hakopod-dev","delete","--raw","/api/v1/namespaces/"+namespace+"/pods/"+pods[0]["metadata"]["name"],"-f","-"],data=delete)
        replacement=None; deadline=time.monotonic()+240
        while time.monotonic()<deadline:
            current=json.loads(self.k("-n",namespace,"get","pods","-l","hakopod.io/neon-role=storage-controller","-o","json"))["items"]
            ready=[pod for pod in current if pod["metadata"]["uid"]!=old_uid and pod.get("status",{}).get("containerStatuses") and all(item.get("ready") for item in pod["status"]["containerStatuses"]) and any(item.get("type")=="Ready" and item.get("status")=="True" for item in pod.get("status",{}).get("conditions",[])) and any(ref.get("kind")=="StatefulSet" and ref.get("uid")==sets[0]["metadata"]["uid"] for ref in pod["metadata"].get("ownerReferences",[]))]
            if len(ready)==1: replacement=ready[0]; break
            time.sleep(2)
        if replacement is None: raise RuntimeError("Neon storage controller did not recover")
        revision=self.api("GET","/api/v1/managed-platforms/"+pid)["revision"]; after=self.api("POST","/api/v1/managed-platforms/"+pid+"/native-probe",{"expected_revision":revision})
        identity=("tenant_id","timeline_id","tenant_generation","timeline_generation")
        if any(before.get(key)!=after.get(key) for key in identity): raise RuntimeError("Neon durable identity changed after controller recovery")
        if self.proxy_query(pid,self.proxy_password(pid),"SELECT 1")!="1": raise RuntimeError("Neon SQL did not recover after controller restart")
        return {"run_id":self.run_id,"platform_id":pid,"namespace_uid":self.bound[pid][0],"controller_pod_uid_before":old_uid,"controller_pod_uid_after":replacement["metadata"]["uid"],"tenant_id":after["tenant_id"],"timeline_id":after["timeline_id"],"tenant_generation":after["tenant_generation"],"timeline_generation":after["timeline_generation"],"identity_preserved":True,"service_recovered":True}
    def object_store_fault(self,action):
        command=load(self.a.object_store_fault_command_file,4096)
        if not isinstance(command,list) or not 1<=len(command)<=8 or not Path(command[0]).is_absolute() or any(not isinstance(item,str) or not item or len(item)>1024 for item in command): raise RuntimeError("object-store fault command is malformed")
        return self.command([*command,action],120).strip()
    def object_store_outage(self,intent):
        self.object_store_fault("stop")
        try:
            review=self.api("POST","/api/v1/managed-platform-recovery/reviews",intent); op=self.api("POST","/api/v1/managed-platform-recovery/operations",dict(intent,review=review),"neon-object-outage-"+self.run_id); operation_path="/api/v1/managed-platform-recovery-operations/"+op["id"]
            try:terminal=self.wait(operation_path,240,("failed",))
            except RuntimeError:
                current=self.api("GET",operation_path)
                if current.get("status") not in {"succeeded","failed","cancelled"}:
                    self.api("POST",operation_path+"/cancel",{},expected=(202,)); self.wait(operation_path,300,("cancelled",))
                raise
            if terminal.get("status")!="failed": raise RuntimeError("Neon backup did not fail during object-store outage")
        finally:self.object_store_fault("start")
        if self.object_store_fault("status")!="running": raise RuntimeError("Neon object store did not recover")
        return {"run_id":self.run_id,"platform_id":intent["source_platform_id"],"namespace_uid":self.bound[intent["source_platform_id"]][0],"outage_observed":True,"backup_refused":True,"service_restored":True}
    def sql(self,ns,pod,statement,expect_success=True,expected_error=None):
        if not expect_success and (not isinstance(expected_error,str) or not expected_error): raise RuntimeError("compute SQL failure contract is missing")
        observed=self.command(["kubectl","--kubeconfig",self.a.kubeconfig,"--context","k3d-hakopod-dev","-n",ns,"exec",pod,"-c","compute","--","psql","-XAt","postgresql://cloud_admin@127.0.0.1:55433/postgres?sslmode=require","-v","ON_ERROR_STOP=1","-c",statement],45,expected=(0,) if expect_success else (1,2,3),capture_stderr=not expect_success).strip()
        if not expect_success and re.search(expected_error,observed,re.IGNORECASE) is None: raise RuntimeError("compute SQL failed for an unexpected reason")
        return observed
    def compute_roles(self,pid,marker):
        namespace,pods=self.pods(pid); computes=sorted((pod for pod in pods if pod["metadata"]["labels"].get("hakopod.io/neon-role")=="compute"),key=lambda pod:pod["metadata"]["labels"].get("app.kubernetes.io/component",""))
        if len(computes)!=2: raise RuntimeError("Neon primary/replica acceptance requires exactly two computes")
        primary,replica=computes
        if [pod["metadata"]["labels"].get("app.kubernetes.io/component") for pod in computes]!=["compute-0","compute-1"]: raise RuntimeError("Neon compute role labels are not exact")
        primary_mode=self.sql(namespace,primary["metadata"]["name"],"SHOW transaction_read_only"); replica_mode=self.sql(namespace,replica["metadata"]["name"],"SHOW transaction_read_only")
        if primary_mode!="off" or replica_mode!="on": raise RuntimeError("Neon compute primary and replica roles are not enforced")
        self.sql(namespace,replica["metadata"]["name"],"INSERT INTO hakopod_native_acceptance VALUES ('replica-write','forbidden')",expect_success=False,expected_error=r"read-only transaction|cannot execute INSERT")
        observed=""; deadline=time.monotonic()+90
        while time.monotonic()<deadline:
            try:observed=self.sql(namespace,replica["metadata"]["name"],"SELECT v FROM hakopod_native_acceptance WHERE k='backup'")
            except RuntimeError:observed=""
            if observed==marker: break
            time.sleep(2)
        if observed!=marker: raise RuntimeError("Neon compute replica did not receive the primary marker")
        return {"run_id":self.run_id,"platform_id":pid,"namespace_uid":self.bound[pid][0],"primary_compute":"compute-0","replica_compute":"compute-1","primary_writable":True,"replica_read_only":True,"replica_write_refused":True,"replica_caught_up":True}
    def compute_pod(self,pid):
        ns,pods=self.pods(pid); matches=[p for p in pods if p["metadata"]["labels"].get("hakopod.io/neon-role")=="compute" and p.get("status",{}).get("containerStatuses") and all(x.get("ready") for x in p["status"]["containerStatuses"]) and any(x.get("type")=="Ready" and x.get("status")=="True" for x in p.get("status",{}).get("conditions",[]))]
        if len(matches)!=self.source_spec["neon"]["compute_replicas"]: raise RuntimeError("ready compute inventory is incomplete")
        matches.sort(key=lambda item:item["metadata"]["labels"].get("app.kubernetes.io/component",""))
        if matches[0]["metadata"]["labels"].get("app.kubernetes.io/component")!="compute-0": raise RuntimeError("writable compute is unavailable")
        return ns,matches[0]["metadata"]["name"]
    def compute_cpu(self,pid,expected):
        namespace,pods=self.pods(pid); result={}
        sets=json.loads(self.k("-n",namespace,"get","statefulsets","-l","hakopod.io/neon-role=compute","-o","json"))["items"]
        owners={item["metadata"]["name"]:item["metadata"]["uid"] for item in sets}
        if set(owners)!={"neon-compute-0","neon-compute-1"}: raise RuntimeError("Neon resource update requires exactly two owned computes")
        for pod in pods:
            metadata=pod["metadata"]; component=metadata.get("labels",{}).get("app.kubernetes.io/component")
            if metadata.get("labels",{}).get("hakopod.io/neon-role")!="compute": continue
            if component not in {"compute-0","compute-1"} or component in result or not any(ref.get("kind")=="StatefulSet" and ref.get("name")=="neon-"+component and ref.get("uid")==owners["neon-"+component] for ref in metadata.get("ownerReferences",[])): raise RuntimeError("Neon resource update compute ownership is invalid")
            containers=[item for item in pod.get("spec",{}).get("containers",[]) if item.get("name")=="compute"]
            ready=pod.get("status",{}).get("containerStatuses",[])
            if len(containers)!=1 or not ready or not all(item.get("ready") is True for item in ready) or not any(item.get("type")=="Ready" and item.get("status")=="True" for item in pod.get("status",{}).get("conditions",[])): raise RuntimeError("Neon resource update compute pod is not ready")
            resources=containers[0].get("resources",{})
            if any(cpu_millis(resources.get(key,{}).get("cpu"))!=expected for key in ("requests","limits")): raise RuntimeError("Neon resource update did not change actual pod CPU")
            result[component]={"pod_uid":metadata["uid"],"cpu_millis":expected}
        if set(result)!={"compute-0","compute-1"}: raise RuntimeError("Neon resource update compute inventory is incomplete")
        return result
    def restored_resource_update(self,pid,marker):
        path="/api/v1/managed-platforms/"+pid; current=self.api("GET",path); revision=current.get("revision")
        if type(revision) is not int or revision<1 or current.get("id")!=pid or current.get("project")!=self.a.project or current.get("environment")!="development" or current.get("spec")!=self.target_spec: raise RuntimeError("Neon resource update target differs from the restored fixture")
        namespace_uid=self.namespace(pid)["metadata"]["uid"]
        if namespace_uid!=self.bound[pid][0]: raise RuntimeError("Neon resource update namespace changed before review")
        before=self.api("POST",path+"/native-probe",{"expected_revision":revision})
        identity=("tenant_id","timeline_id","tenant_generation","timeline_generation")
        if before.get("platform_id")!=pid or before.get("platform_revision")!=revision or before.get("namespace_uid")!=namespace_uid or any(not ID.fullmatch(str(before.get(key))) for key in identity[:2]) or any(type(before.get(key)) is not int or before[key]<1 for key in identity[2:]): raise RuntimeError("Neon resource update initial provider identity is invalid")
        updated=json.loads(json.dumps(current["spec"])); previous_cpu=cpu_millis(updated["resources"]["compute"]["cpu"]); requested_cpu=previous_cpu+100
        if not 100<=previous_cpu<=15900: raise RuntimeError("Neon resource update exceeds the bounded CPU probe")
        pods_before=self.compute_cpu(pid,previous_cpu); updated["resources"]["compute"]["cpu"]=str(requested_cpu)+"m"
        body={"id":pid,"project":self.a.project,"environment":"development","expected_revision":revision,"kind":"update","confirm_name":current["spec"]["name"],"spec":updated}
        reviewed=self.api("POST","/api/v1/managed-platforms/reviews",body)
        if reviewed.get("blocked") is not False or not ID.fullmatch(str(reviewed.get("review",{}).get("id"))) or reviewed["review"].get("expected_revision")!=revision or reviewed.get("platform",{}).get("id")!=pid or reviewed["platform"].get("spec")!=updated: raise RuntimeError("Neon resource update review did not bind the exact request")
        request=dict(body,review=reviewed["review"]); accepted=self.api("POST","/api/v1/managed-platforms/operations",request,"neon-resource-update-"+self.run_id,expected=(202,))
        operation=self.wait("/api/v1/managed-platform-operations/"+accepted["id"])
        if operation.get("id")!=accepted["id"] or operation.get("platform_id")!=pid or operation.get("kind")!="update" or operation.get("revision")!=revision+1: raise RuntimeError("Neon resource update operation binding is invalid")
        changed=self.api("GET",path)
        if changed.get("revision")!=revision+1 or changed.get("spec")!=updated: raise RuntimeError("Neon resource update did not advance exactly one revision")
        stale=self.api("POST","/api/v1/managed-platforms/operations",request,"neon-resource-stale-"+self.run_id,expected=(409,))
        if stale.get("error",{}).get("code")!="conflict": raise RuntimeError("Neon stale resource update failed for an unexpected reason")
        topology=json.loads(json.dumps(updated)); topology["neon"]["compute_replicas"]=3
        negative=dict(body,expected_revision=revision+1,spec=topology)
        rejected=self.api("POST","/api/v1/managed-platforms/reviews",negative,expected=(400,))
        if rejected.get("error",{}).get("code")!="invalid_request": raise RuntimeError("Neon topology change failed for an unexpected reason")
        final=self.api("GET",path); after=self.api("POST",path+"/native-probe",{"expected_revision":revision+1})
        if final.get("revision")!=revision+1 or final.get("spec")!=updated or self.namespace(pid)["metadata"]["uid"]!=namespace_uid or after.get("platform_id")!=pid or after.get("platform_revision")!=revision+1 or after.get("namespace_uid")!=namespace_uid or any(after.get(key)!=before[key] for key in identity): raise RuntimeError("Neon resource update changed restored identity or rejected requests advanced the revision")
        pods_after=self.compute_cpu(pid,requested_cpu)
        if any(pods_before[name]["pod_uid"]==pods_after[name]["pod_uid"] for name in pods_before): raise RuntimeError("Neon resource update did not replace compute pods")
        roles=self.compute_roles(pid,marker)
        if self.proxy_query(pid,self.proxy_password(pid),"SELECT v FROM hakopod_native_acceptance WHERE k='backup'")!=marker: raise RuntimeError("Neon resource update lost the restored SQL marker")
        namespace,primary=self.compute_pod(pid)
        self.sql(namespace,primary,"INSERT INTO hakopod_native_acceptance VALUES ('resource-update','written') ON CONFLICT (k) DO UPDATE SET v=excluded.v")
        if self.proxy_query(pid,self.proxy_password(pid),"SELECT v FROM hakopod_native_acceptance WHERE k='resource-update'")!="written": raise RuntimeError("Neon updated primary did not commit a write")
        return {"run_id":self.run_id,"platform_id":pid,"namespace_uid":namespace_uid,"operation_id":operation["id"],"revision_before":revision,"revision_after":revision+1,"compute_cpu_millis_before":previous_cpu,"compute_cpu_millis_after":requested_cpu,"compute_pods_before":pods_before,"compute_pods_after":pods_after,**{key:after[key] for key in identity},"identity_preserved":True,"stale_revision_refused":True,"topology_change_refused":True,"primary_compute":roles["primary_compute"],"replica_compute":roles["replica_compute"],"primary_writable":True,"replica_read_only":roles["replica_read_only"],"replica_write_refused":roles["replica_write_refused"],"replica_caught_up":roles["replica_caught_up"],"restored_data_sha256":hashlib.sha256(marker.encode()).hexdigest()}
    def recovery_receipt(self,value,operation,artifact,source,source_revision,source_uid,target,target_revision):
        required={"operation_id","status","artifact_id","manifest_sha256","source_platform_id","source_revision","source_namespace_uid","target_platform_id","target_revision","format","parts","neon"}
        if set(value)!=required or value.get("operation_id")!=operation or value.get("status")!="succeeded" or value.get("artifact_id")!=artifact or not DIGEST.fullmatch(str(value.get("manifest_sha256"))) or value.get("source_platform_id")!=source or value.get("source_revision")!=source_revision or value.get("source_namespace_uid")!=source_uid or value.get("target_platform_id")!=target or value.get("target_revision")!=target_revision or value.get("format")!="hakopod-neon-recovery-v1" or value.get("parts")!=["tenant.json","timeline.json","remote-storage.tar"] or not isinstance(value.get("neon"),dict): raise RuntimeError("native recovery receipt binding is invalid")
        return value["neon"]
    def cancellation_receipt(self,value,operation,artifact,manifest,source,source_revision,target,target_revision,target_uid):
        required={"schema_version","operation_id","project","environment","source_platform_id","source_revision","target_platform_id","target_revision","artifact_id","manifest_sha256","tenant_id","timeline_id","tenant_generation","timeline_generation","journal_entries","journal_phase_counts","cleanup_pending","operation_authority_refused","namespace_uid","deployment_count","statefulset_count","workload_replicas_zero","pods_absent","staging_prefix_empty"}
        counts=value.get("journal_phase_counts")
        topology=self.cancellation_target_spec["neon"]; expected_sets=topology["compute_replicas"]+topology["pageservers"]+topology["safekeepers"]
        if set(value)!=required or value.get("schema_version")!=1 or value.get("operation_id")!=operation or value.get("project")!=self.a.project or value.get("environment")!="development" or value.get("source_platform_id")!=source or value.get("source_revision")!=source_revision or value.get("target_platform_id")!=target or value.get("target_revision")!=target_revision or value.get("artifact_id")!=artifact or value.get("manifest_sha256")!=manifest or value.get("namespace_uid")!=target_uid or not ID.fullmatch(str(value.get("tenant_id"))) or not ID.fullmatch(str(value.get("timeline_id"))) or type(value.get("tenant_generation")) is not int or value["tenant_generation"]<1 or type(value.get("timeline_generation")) is not int or value["timeline_generation"]<1 or type(value.get("journal_entries")) is not int or value["journal_entries"]<1 or not isinstance(counts,dict) or set(counts)!={"complete","empty_complete","untouched_complete"} or any(type(count) is not int or count<0 for count in counts.values()) or sum(counts.values())!=value["journal_entries"] or value.get("cleanup_pending") is not False or value.get("operation_authority_refused") is not True or value.get("deployment_count")!=1 or value.get("statefulset_count")!=expected_sets or value.get("workload_replicas_zero") is not True or value.get("pods_absent") is not True or value.get("staging_prefix_empty") is not True: raise RuntimeError("native cancellation receipt binding is invalid")
        return value
    def tenant_migration(self,pid,uid):
        path="/api/v1/managed-platforms/"+pid; current=self.api("GET",path); revision=current.get("revision")
        before=self.api("POST",path+"/native-probe",{"expected_revision":revision})
        active=restart_storage(before,pid,revision,uid); source=active["node_id"]
        pageservers=current.get("spec",{}).get("neon",{}).get("pageservers")
        if type(pageservers) is not int or not 2<=pageservers<=8 or source>pageservers: raise RuntimeError("Neon migration pageserver inventory is invalid")
        destination=next(node for node in range(1,pageservers+1) if node!=source)
        def computes():
            namespace,pods=self.pods(pid)
            sets=json.loads(self.k("-n",namespace,"get","statefulsets","-l","hakopod.io/neon-role=compute","-o","json"))["items"]
            owners={item["metadata"]["name"]:item["metadata"]["uid"] for item in sets}; result={}
            if set(owners)!={"neon-compute-0","neon-compute-1"}: raise RuntimeError("Neon migration requires exactly two owned computes")
            for pod in pods:
                meta=pod["metadata"]; labels=meta.get("labels",{}); component=labels.get("app.kubernetes.io/component")
                if labels.get("hakopod.io/neon-role")!="compute": continue
                if component not in {"compute-0","compute-1"} or component in result or not any(ref.get("kind")=="StatefulSet" and ref.get("name")=="neon-"+component and ref.get("uid")==owners["neon-"+component] for ref in meta.get("ownerReferences",[])): raise RuntimeError("Neon migration compute ownership is invalid")
                statuses=pod.get("status",{}).get("containerStatuses",[])
                if not statuses or not all(item.get("ready") is True for item in statuses): raise RuntimeError("Neon migration compute is not ready")
                result[component]=meta["name"]
            if set(result)!={"compute-0","compute-1"}: raise RuntimeError("Neon migration compute inventory is incomplete")
            return namespace,result
        namespace,pods=computes(); marker="hakopod-migration-"+self.run_id
        self.sql(namespace,pods["compute-0"],"CREATE TABLE IF NOT EXISTS hakopod_native_acceptance(k text primary key,v text not null); INSERT INTO hakopod_native_acceptance VALUES ('migration','"+marker+"') ON CONFLICT (k) DO UPDATE SET v=excluded.v;")
        if self.namespace(pid)["metadata"]["uid"]!=uid: raise RuntimeError("Neon migration namespace changed")
        moved=self.api("POST",path+"/native-migrate",{"expected_revision":revision,"namespace_uid":uid,"source_node_id":source,"destination_node_id":destination})
        expected={"platform_id":pid,"platform_revision":revision,"namespace_uid":uid,"tenant_id":before["tenant_id"],"timeline_id":before["timeline_id"],"source_node_id":source,"destination_node_id":destination,"generation_before":before["tenant_generation"],"tenant_owner_unchanged":True,"controller_move_completed":True,"compute_names":["compute-0","compute-1"],"compute_routing_verified":True}
        if set(moved)!=set(expected)|{"generation_after"} or any(moved.get(key)!=value for key,value in expected.items()) or type(moved.get("generation_after")) is not int or moved["generation_after"]<=before["tenant_generation"]: raise RuntimeError("Neon migration observation is not exact")
        namespace,pods=computes(); primary=self.sql(namespace,pods["compute-0"],"SELECT v FROM hakopod_native_acceptance WHERE k='migration'")
        replica=""; deadline=time.monotonic()+90
        while time.monotonic()<deadline:
            try: replica=self.sql(namespace,pods["compute-1"],"SELECT v FROM hakopod_native_acceptance WHERE k='migration'")
            except RuntimeError: replica=""
            if replica==marker: break
            time.sleep(2)
        if primary!=marker or replica!=marker: raise RuntimeError("Neon migration did not preserve SQL on both computes")
        after=self.api("POST",path+"/native-probe",{"expected_revision":revision})
        if restart_storage(after,pid,revision,uid)["node_id"]!=destination or after.get("tenant_generation")!=moved["generation_after"] or after.get("tenant_id")!=before["tenant_id"] or after.get("timeline_id")!=before["timeline_id"]: raise RuntimeError("Neon migration placement changed during SQL verification")
        return dict(moved,run_id=self.run_id,primary_sql_verified=True,replica_sql_verified=True,data_sha256=hashlib.sha256(marker.encode()).hexdigest())
    def restart(self,pid,uid):
        path="/api/v1/managed-platforms/"+pid; revision=self.api("GET",path)["revision"]
        active_storage=restart_storage(self.api("POST",path+"/native-probe",{"expected_revision":revision}),pid,revision,uid)
        ns,pods=self.pods(pid); chosen={}; components={"pageserver":active_storage["component"],"compute":"compute-0"}
        sets=json.loads(self.k("-n",ns,"get","statefulsets","-l","hakopod.io/managed-platform-id="+pid,"-o","json"))["items"]
        if len(sets)>16: raise RuntimeError("Neon restart StatefulSet inventory exceeded its bound")
        owners={item["metadata"]["name"]:item["metadata"]["uid"] for item in sets}
        for pod in pods:
            role=pod["metadata"]["labels"].get("hakopod.io/neon-role"); component=pod["metadata"]["labels"].get("app.kubernetes.io/component")
            if role not in components or component!=components[role]: continue
            owner="neon-"+component
            if role in chosen or owner not in owners or not any(ref.get("kind")=="StatefulSet" and ref.get("name")==owner and ref.get("uid")==owners[owner] for ref in pod["metadata"].get("ownerReferences",[])): raise RuntimeError("Neon restart subject ownership is invalid")
            chosen[role]=pod
        before={k:v["metadata"]["uid"] for k,v in chosen.items()}
        if set(before)!={"pageserver","compute"}: raise RuntimeError("restart subjects are missing")
        if self.namespace(pid)["metadata"]["uid"]!=uid: raise RuntimeError("Neon restart namespace changed")
        seed="hakopod-"+self.run_id; self.sql(ns,chosen["compute"]["metadata"]["name"],"CREATE TABLE IF NOT EXISTS hakopod_native_acceptance(k text primary key,v text not null); INSERT INTO hakopod_native_acceptance VALUES ('restart','"+seed+"') ON CONFLICT (k) DO UPDATE SET v=excluded.v;")
        if restart_storage(self.api("POST",path+"/native-probe",{"expected_revision":revision}),pid,revision,uid)!=active_storage: raise RuntimeError("Neon active storage moved before the restart")
        for pod in chosen.values():
            delete=json.dumps({"apiVersion":"v1","kind":"DeleteOptions","preconditions":{"uid":pod["metadata"]["uid"]}}).encode()
            self.command(["kubectl","--request-timeout=15s","--kubeconfig",self.a.kubeconfig,"--context","k3d-hakopod-dev","delete","--raw","/api/v1/namespaces/"+ns+"/pods/"+pod["metadata"]["name"],"-f","-"],data=delete)
        after={}; old_absent=False; failure_observed=False; deadline=time.monotonic()+300
        while time.monotonic()<deadline:
            current=json.loads(self.k("-n",ns,"get","pods","-o","json"))["items"]; after={}
            if len(current)>32: raise RuntimeError("Neon restart pod inventory exceeded its bound")
            current_uids={pod["metadata"]["uid"] for pod in current}; old_absent=not set(before.values())&current_uids; failure_observed = failure_observed or old_absent
            for pod in current:
                metadata=pod["metadata"]; labels=metadata.get("labels",{}); role=labels.get("hakopod.io/neon-role"); component=labels.get("app.kubernetes.io/component")
                if role not in components or component!=components[role] or metadata["name"]!=chosen[role]["metadata"]["name"] or metadata["uid"]==before[role]: continue
                owner="neon-"+component; statuses=pod.get("status",{}).get("containerStatuses",[])
                ready=bool(statuses) and all(item.get("ready") is True for item in statuses) and any(item.get("type")=="Ready" and item.get("status")=="True" for item in pod.get("status",{}).get("conditions",[]))
                if not any(ref.get("kind")=="StatefulSet" and ref.get("name")==owner and ref.get("uid")==owners[owner] for ref in metadata.get("ownerReferences",[])): raise RuntimeError("Neon restart replacement ownership changed")
                if ready:
                    if role in after: raise RuntimeError("Neon restart replacement inventory is not exact")
                    after[role]=pod
            if old_absent and set(after)==set(before): break
            time.sleep(2)
        if not old_absent or set(after)!=set(before) or self.namespace(pid)["metadata"]["uid"]!=uid: raise RuntimeError("owned pods did not recover")
        restored=self.sql(ns,after["compute"]["metadata"]["name"],"SELECT v FROM hakopod_native_acceptance WHERE k='restart';")
        if restored!=seed or not failure_observed: raise RuntimeError("restart failure and restored SQL data were not both observed")
        digest=hashlib.sha256(restored.encode()).hexdigest()
        return {"run_id":self.run_id,"platform_id":pid,"namespace_uid":uid,"storage_pod_uids_before":[before["pageserver"]],"storage_pod_uids_after":[after["pageserver"]["metadata"]["uid"]],"compute_pod_uids_before":[before["compute"]],"compute_pod_uids_after":[after["compute"]["metadata"]["uid"]],"active_storage":active_storage,"restarted_components":components,"statefulset_uids":{role:owners["neon-"+component] for role,component in components.items()},"old_pods_absent":old_absent,"failure_observed":failure_observed,"service_recovered":restored==seed,"data_sha256":digest}
    def run(self):
        attestation=validate_attestation(self.a); nodes=json.loads(self.k("get","nodes","-o","json"))["items"]
        observed_nodes={item["metadata"]["name"]:item["metadata"]["uid"] for item in nodes}; cluster_uid=self.namespace_uid("kube-system")
        if observed_nodes!=attestation["node_uids"] or cluster_uid!=attestation["cluster_uid"]: raise RuntimeError("attested cluster identity changed")
        self.run_id=self.command([sys.executable,str(Path(__file__).with_name("evidence.py")),"begin","--source",self.a.source,"--images",self.a.images,"--identities",self.a.identities,"--state",str(self.evidence/"state.json"),"--events",str(self.evidence/"events.json"),"--context","k3d-hakopod-dev"]).strip()
        source,sop=self.create(self.source_spec); target,top=self.create(self.target_spec); self.proxy_passwords={source["id"]:self.proxy_password_files["source"],target["id"]:self.proxy_password_files["target"]}
        for role,platform,op in (("source",source,sop),("recovery_target",target,top)):
            namespace=self.namespace(platform["id"]); self.bound[platform["id"]]=(namespace["metadata"]["uid"],op["id"])
            path=self.evidence/(role+"-create.json"); atomic(path,{k:op[k] for k in ("id","platform_id","kind","status")}); self.command([sys.executable,str(Path(__file__).with_name("evidence.py")),"bind-resource","--state",str(self.evidence/"state.json"),"--role",role,"--operation",str(path)])
        self.command([sys.executable,str(Path(__file__).with_name("evidence.py")),"observe-identities","--state",str(self.evidence/"state.json")],180)
        source_uid=self.namespace(source["id"])["metadata"]["uid"]; self.record("ownership-capability",self.ownership(source["id"],sop["id"])); tls,lifecycle=self.runtime(source["id"],source_uid); source_current=self.api("GET","/api/v1/managed-platforms/"+source["id"]); tls["managed_tls"]=self.managed_tls(source["id"],source_current["revision"],sop["id"]); tls["control_plane_trust"]=self.control_plane_trust(source["id"]); self.record("tls",tls); self.pending_lifecycle=lifecycle
        source_ns,source_compute=self.compute_pod(source["id"]); seed="backup-"+self.run_id; self.sql(source_ns,source_compute,"CREATE TABLE IF NOT EXISTS hakopod_native_acceptance(k text primary key,v text not null); INSERT INTO hakopod_native_acceptance VALUES ('backup','"+seed+"') ON CONFLICT (k) DO UPDATE SET v=excluded.v;")
        self.record("compute-roles",self.compute_roles(source["id"],seed)); self.record("isolation-authentication",self.isolation_and_credentials(source["id"],target["id"],seed)); self.record("connection-limits",self.connection_limits(source["id"]))
        sc=self.api("GET","/api/v1/managed-platforms/"+source["id"]); tc=self.api("GET","/api/v1/managed-platforms/"+target["id"])
        backup_intent={"kind":"backup","project":self.a.project,"environment":"development","source_platform_id":source["id"],"destination_id":self.a.destination_id,"destination_revision":self.a.destination_revision,"expected_source_revision":sc["revision"]}; self.record("object-store-outage",self.object_store_outage(backup_intent)); backup=self.recover(backup_intent,"backup"); artifact=backup.get("result_artifact_id")
        if not ID.fullmatch(str(artifact)): raise RuntimeError("backup did not publish an artifact")
        intent={"kind":"restore","project":self.a.project,"environment":"development","source_platform_id":source["id"],"target_platform_id":target["id"],"artifact_id":artifact,"expected_source_revision":sc["revision"],"expected_target_revision":tc["revision"],"confirm_target_name":self.target_spec["name"]}; restored=self.recover(intent,"restore")
        receipt=self.api("GET","/api/v1/managed-platform-recovery-operations/"+restored["id"]+"/native-receipt"); neon=self.recovery_receipt(receipt,restored["id"],artifact,source["id"],sc["revision"],source_uid,target["id"],tc["revision"]); target_uid=self.namespace(target["id"])["metadata"]["uid"]
        target_probe=self.api("POST","/api/v1/managed-platforms/"+target["id"]+"/native-probe",{"expected_revision":tc["revision"]})
        if target_probe.get("platform_id")!=target["id"] or target_probe.get("platform_revision")!=tc["revision"] or target_probe.get("namespace_uid")!=target_uid or target_uid==source_uid or target_probe.get("tenant_id")!=neon.get("tenant_id") or target_probe.get("timeline_id")!=neon.get("timeline_id"): raise RuntimeError("restored target provider identity is not isolated or artifact-bound")
        target_ns,target_compute=self.compute_pod(target["id"]); restored_value=self.sql(target_ns,target_compute,"SELECT v FROM hakopod_native_acceptance WHERE k='backup';")
        if restored_value!=seed: raise RuntimeError("restored target SQL did not match the pre-backup source value")
        evidence={"run_id":self.run_id,"platform_id":source["id"],"namespace_uid":source_uid,"target_platform_id":target["id"],"target_namespace_uid":target_uid,"format":receipt.get("format"),"parts":receipt.get("parts"),"tenant_id":neon.get("tenant_id"),"timeline_id":neon.get("timeline_id"),"tenant_generation":neon.get("tenant_generation"),"timeline_generation":neon.get("timeline_generation"),"commit_lsn":neon.get("commit_lsn"),"pageserver_remote_consistent_lsns":neon.get("pageserver_remote_consistent_lsns"),"source_object_prefix":neon.get("source_object_prefix"),"object_inventory_sha256":neon.get("object_inventory_sha256"),"object_count":neon.get("object_count"),"object_bytes":neon.get("object_bytes"),"restored_data_sha256":hashlib.sha256(restored_value.encode()).hexdigest(),"isolated_target":receipt.get("target_platform_id")==target["id"] and target_uid!=source_uid}; self.record("backup-recovery",evidence)
        self.record("restored-resource-update",self.restored_resource_update(target["id"],seed))
        self.record("wal-quorum-fencing",self.wal_quorum_fencing(target["id"])); self.record("controller-recovery",self.controller_recovery(target["id"])); self.record("tenant-migration",self.tenant_migration(target["id"],target_uid)); self.record("restart-failure",self.restart(target["id"],target_uid))
        cancellation,cop=self.create(self.cancellation_target_spec); self.proxy_passwords[cancellation["id"]]=self.proxy_password_files["cancellation"]; cancellation_uid=self.namespace(cancellation["id"])["metadata"]["uid"]; self.bound[cancellation["id"]]=(cancellation_uid,cop["id"])
        path=self.evidence/"cancellation-target-create.json"; atomic(path,{k:cop[k] for k in ("id","platform_id","kind","status")}); self.command([sys.executable,str(Path(__file__).with_name("evidence.py")),"bind-resource","--state",str(self.evidence/"state.json"),"--role","cancellation_target","--operation",str(path)])
        cc=self.api("GET","/api/v1/managed-platforms/"+cancellation["id"]); cancel_intent=dict(intent,target_platform_id=cancellation["id"],expected_target_revision=cc["revision"],confirm_target_name=self.cancellation_target_spec["name"])
        review=self.api("POST","/api/v1/managed-platform-recovery/reviews",cancel_intent); op=self.api("POST","/api/v1/managed-platform-recovery/operations",dict(cancel_intent,review=review),"neon-cancel-"+self.run_id); operation_path="/api/v1/managed-platform-recovery-operations/"+op["id"]; self.wait_phase(operation_path,{"target-admitted","restoring-tenant.json","restoring-timeline.json","restoring-remote-storage.tar"}); self.api("POST",operation_path+"/cancel",{},expected=(202,)); self.wait(operation_path,300,("cancelled",))
        cancellation_receipt=self.api("GET","/api/v1/managed-platform-recovery-operations/"+op["id"]+"/native-cancellation-receipt"); observed=self.cancellation_receipt(cancellation_receipt,op["id"],artifact,receipt["manifest_sha256"],source["id"],sc["revision"],cancellation["id"],cc["revision"],cancellation_uid)
        self.pending_revocation={"run_id":self.run_id,"platform_id":source["id"],"namespace_uid":source_uid,"restore_operation_id":observed["operation_id"],"cancellation_target_namespace_uid":observed["namespace_uid"],**{key:value for key,value in observed.items() if key not in ("operation_id","namespace_uid")}}
        spec=importlib.util.spec_from_file_location("neon_partial_create",Path(__file__).with_name("partial-create.py"))
        partial=importlib.util.module_from_spec(spec); spec.loader.exec_module(partial)
        partial.run(self,source,target,atomic)
    def namespace_uid(self,name):
        value=json.loads(self.k("get","namespace",name,"-o","json")); return value.get("metadata",{}).get("uid","")
    def cleanup(self):
        if self.cleanup_completed: return
        if self.cleanup_started: raise RuntimeError("owned cleanup is already active")
        self.cleanup_started=True
        failed=[]
        namespaces={"managed-platform-"+pid for pid,unused,unused_op in self.platforms}
        owned_volumes={}
        volume_audit_complete=True
        try:
            before=json.loads(self.k("get","pv","--chunk-size=128","-o","json")).get("items",[])
            if len(before)>512: failed.append("persistent-volumes"); volume_audit_complete=False
            else:
                for item in before:
                    metadata=item.get("metadata",{}); claim=item.get("spec",{}).get("claimRef",{})
                    if claim.get("namespace") in namespaces and metadata.get("name") and metadata.get("uid"): owned_volumes[(metadata["name"],metadata["uid"])]={"name":metadata["name"],"uid":metadata["uid"],"claim_namespace":claim.get("namespace"),"claim_name":claim.get("name")}
        except Exception:failed.append("persistent-volumes"); volume_audit_complete=False
        for pid,name,opid in reversed(self.platforms):
            try:
                present=self.claim_namespace(pid,opid); expected=self.bound.get(pid)
                if present:
                    current=self.namespace(pid)
                    if expected is None or current["metadata"]["uid"]!=expected[0] or current["metadata"].get("labels",{}).get("hakopod.io/owner-operation-id")!=expected[1]: raise RuntimeError("cleanup target identity changed")
                if pid not in getattr(self,"deleted_platforms",set()): self.delete(pid,name,240)
            except Exception:failed.append(pid)
        remaining_namespaces=[]
        namespace_audit_complete=True
        for pid,unused,unused_op in self.platforms:
            try:
                if self.k("get","namespace","managed-platform-"+pid,"--ignore-not-found","-o","name").strip(): failed.append(pid); remaining_namespaces.append("managed-platform-"+pid)
            except Exception:failed.append(pid); namespace_audit_complete=False
        remaining_volumes=[]
        try:
            volumes=json.loads(self.k("get","pv","--chunk-size=128","-o","json")).get("items",[])
            if len(volumes)>512: failed.append("persistent-volumes"); volume_audit_complete=False
            else:
                for item in volumes:
                    metadata=item.get("metadata",{}); claim=item.get("spec",{}).get("claimRef",{}); identity=(metadata.get("name"),metadata.get("uid"))
                    if identity in owned_volumes: remaining_volumes.append(owned_volumes[identity])
                    elif claim.get("namespace") in namespaces: remaining_volumes.append({"name":metadata.get("name"),"uid":metadata.get("uid"),"claim_namespace":claim.get("namespace"),"claim_name":claim.get("name")})
                if remaining_volumes: failed.append("persistent-volumes")
        except Exception:failed.append("persistent-volumes"); volume_audit_complete=False
        if not failed and self.pending_lifecycle is not None:
            self.pending_lifecycle.update(stopped=True,deleted=True,branch_deleted=True); self.record("tenant-timeline-compute-lifecycle",self.pending_lifecycle); self.pending_lifecycle=None
        if self.foreign is not None:
            namespace,uid,name=self.foreign
            try:
                current=self.namespace_uid(namespace)
                preserved=current==uid and json.loads(self.k("-n",namespace,"get","configmap",name,"-o","json")).get("data")=={"owner":"foreign"}
                if not preserved: raise RuntimeError("foreign sentinel did not survive platform teardown")
                if self.pending_revocation is not None:
                    self.pending_revocation["foreign_resources_preserved"]=True; self.record("revocation-cleanup",self.pending_revocation); self.pending_revocation=None
                if self.namespace_uid(namespace)!=uid: raise RuntimeError("foreign sentinel namespace identity changed")
                self.k("delete","namespace",namespace,"--wait=true",timeout=120)
                if self.k("get","namespace",namespace,"--ignore-not-found","-o","name").strip(): raise RuntimeError("foreign sentinel cleanup is incomplete")
                self.foreign=None
            except Exception: failed.append(namespace)
        resources=[{"platform_id":pid,"namespace":"managed-platform-"+pid,"namespace_uid":self.bound.get(pid,(None,None))[0],"create_operation_id":opid} for pid,unused,opid in self.platforms]
        if failed:
            # The durable delete may outlive this wait and still needs its provider transport.
            atomic(self.evidence/"cleanup-transport-retained.json",{"schema_version":1,"status":"retained","run_id":self.run_id,"resources":resources,"failure_categories":sorted(set(failed))})
        else:
            try:self.command(self.bridge_argv("cleanup"),120)
            except Exception:failed.append("control-plane-bridge")
        atomic(self.evidence/"cleanup-attempt.json",{"schema_version":1,"status":"incomplete" if failed else "verified","run_id":self.run_id,"resources":resources,"owned_persistent_volumes":[owned_volumes[key] for key in sorted(owned_volumes)],"namespaces_absent":namespace_audit_complete and not remaining_namespaces,"persistent_volume_claim_refs_absent":volume_audit_complete and not remaining_volumes,"remaining_namespaces":sorted(remaining_namespaces),"remaining_persistent_volumes":remaining_volumes,"failure_categories":sorted(set(failed))})
        if failed: raise RuntimeError("owned cleanup is incomplete")
        self.cleanup_completed=True

    def record_failure(self,code):
        state=self.evidence/"state.json"
        if state.is_file() and not state.is_symlink():
            try:self.command([sys.executable,str(Path(__file__).with_name("evidence.py")),"fail","--state",str(state),"--code",code])
            except Exception:pass

def parse(argv=None):
    p=argparse.ArgumentParser(description=__doc__)
    for name in ("source","kubeconfig","api-url","token-file","source-spec","target-spec","cancellation-target-spec","images","identities","destination-id","work-dir","gate-attestation","project","managed-tls-helper","runtime-spec-digest-helper","control-psql-command-file","control-plane-bridge","object-store-fault-command-file","openssl","psql","source-proxy-password-file","target-proxy-password-file","cancellation-proxy-password-file"):p.add_argument("--"+name,required=True)
    p.add_argument("--scheduling-policy",default="")
    p.add_argument("--destination-revision",required=True,type=int); p.add_argument("--local-port",required=True,type=int); return p.parse_args(argv)

def main(argv=None):
    driver=None
    previous={}
    def interrupted(number,frame): raise TerminationRequested("termination signal received")
    try:
        a=parse(argv); driver=Driver(a)
        for number in (signal.SIGINT,signal.SIGTERM): previous[number]=signal.signal(number,interrupted)
        driver.run()
        for number in (signal.SIGINT,signal.SIGTERM): signal.signal(number,signal.SIG_IGN)
        driver.cleanup(); log=driver.evidence/"sanitized.log"; log.write_text("Neon native acceptance completed; secrets and raw output withheld.\n"); os.chmod(log,0o600)
        driver.command([sys.executable,str(Path(__file__).with_name("evidence.py")),"finalize","--source",a.source,"--state",str(driver.evidence/"state.json"),"--events",str(driver.evidence/"events.json"),"--sanitized-log",str(log),"--report",str(driver.evidence/"report.json"),"--cleanup",str(driver.evidence/"cleanup.json")]); print(driver.evidence/"report.json"); return 0
    except (Exception,KeyboardInterrupt) as error:
        if driver:
            for number in (signal.SIGINT,signal.SIGTERM): signal.signal(number,signal.SIG_IGN)
            driver.record_failure("termination-requested" if isinstance(error,(TerminationRequested,KeyboardInterrupt)) else "acceptance-failed")
            try:driver.cleanup()
            except Exception:pass
        print("Neon native acceptance failed: "+str(error),file=sys.stderr); return 1
    finally:
        for number,handler in previous.items(): signal.signal(number,handler)
if __name__=="__main__": raise SystemExit(main())
